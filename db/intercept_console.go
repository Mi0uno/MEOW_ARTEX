package db

import (
	"fmt"
	"strings"
)

// 本文件是审批台(GET /api/intercept/console,契约 APPROVAL_CONSOLE_CONTRACTS §1)
// 的查询侧:分页行复用 intercept.go 的 approvalRowSelect 保证列一致;计数与工具
// 清单是同条件的聚合,total 先于分页计算(先过滤后分页,报告中心老教训)。

// ConsoleToolCount 是审批台工具下拉的一行,字段名与 web/src/lib/types.ts 的
// InterceptToolCount 严格对齐。
type ConsoleToolCount struct {
	Tool  string `json:"tool"`
	Count int    `json:"count"`
}

// interceptConsoleStatuses 是 counts 的五档固定口径,与 status 白名单一致
// (server 侧 interceptConsoleFilterParams 同名单)。
var interceptConsoleStatuses = [...]string{"pending", "allowed", "denied", "timeout", "flagged"}

// CompleteInterceptCounts 把 GROUP BY status 的结果补齐成恒有五档的 map:缺档补 0,
// 库里不可能出现的未知档丢弃,nil 输入也返回非 nil——nil map 会序列化成 JSON null,
// 前端对 null 取键会崩(本项目已踩过)。抽成纯函数便于单测。
func CompleteInterceptCounts(raw map[string]int) map[string]int {
	out := make(map[string]int, len(interceptConsoleStatuses))
	for _, s := range interceptConsoleStatuses {
		out[s] = raw[s]
	}
	return out
}

// likeEscaper 转义 LIKE/ILIKE 的全部三个特殊字符。NewReplacer 是单趟替换,写入的
// 反斜杠不会被再次转义。
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// EscapeLikePattern 把用户输入变成 ILIKE 模式的字面量:\ % _ 各自前置反斜杠,
// 配合 SQL 侧的 ESCAPE '\' 使用,用户输入里的通配符按字面匹配。
func EscapeLikePattern(s string) string {
	return likeEscaper.Replace(s)
}

// approvalCountFrom 是过滤计数与 Q 搜索聚合用的 FROM 子句。Q 会搜任务名(t.name)
// 与会话标题(c.title),所以聚合也要带这两个 LEFT JOIN;都是 PK 对一关联,不会
// 放大行数,不带 Q 的旧调用计数结果与 join 前完全一致。
const approvalCountFrom = ` FROM intercept_pending ip
LEFT JOIN conversations c ON c.id = ip.conversation_id
LEFT JOIN tasks t ON t.id::text = ip.task_id`

// interceptApprovalConds 拼拦截审批行共用的 WHERE 条件。fixedTaskID 是
// ListTaskInterceptsPage 的路径参数,与 filter.TaskID 叠加(各自加条件,互不冲突);
// withStatus/withTool 控制对应维度是否参与过滤:审批台的 counts 按 status 分组、
// tools 按 tool_name 分组,分组维度自身不能进条件(契约 §1)。返回值直接接在
// FROM/SELECT 之后使用。
func interceptApprovalConds(fixedTaskID string, filter InterceptApprovalFilter, withStatus, withTool bool) (string, []any) {
	conditions := []string{}
	args := []any{}
	add := func(column, value string) {
		if value != "" {
			args = append(args, value)
			conditions = append(conditions, column+"=$"+fmt.Sprint(len(args)))
		}
	}
	add("ip.task_id", fixedTaskID)
	if withTool {
		add("ip.task_id", filter.TaskID)
		add("ip.tool_name", filter.Tool)
	}
	if withStatus {
		add("ip.status", filter.Status)
	}
	add(approvalDecisionSource, filter.DecisionSource)
	// Q 模糊搜索:同一占位号在五个被搜列间复用,先转义再拼 '%..%',ESCAPE '\'
	// 让用户输入里的 % _ \ 按字面匹配。
	if q := strings.TrimSpace(filter.Q); q != "" {
		args = append(args, EscapeLikePattern(q))
		n := fmt.Sprint(len(args))
		pat := "'%'||$" + n + "||'%' ESCAPE '\\'"
		conditions = append(conditions, "(ip.tool_name ILIKE "+pat+
			" OR ip.reason ILIKE "+pat+
			" OR COALESCE(t.name,'') ILIKE "+pat+
			" OR COALESCE(c.title,'') ILIKE "+pat+
			" OR ip.tool_input::text ILIKE "+pat+")")
	}
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}
	return where, args
}

// clampInterceptPage 钳制分页参数:page>=1、1<=size<=100(缺省 20)。
// listInterceptsPage 与 ConsoleInterceptsPage 共用,保证两处口径一致。
func clampInterceptPage(page, size int) (int, int) {
	if page < 1 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	return page, size
}

// ConsoleInterceptsPage 返回审批台一页:分页行 + 过滤后总数 + 五档计数 + 工具清单。
// counts 不受 status 影响、tools 不受 tool 影响(各自的分组维度,契约 §1)。
// items/counts/tools 永不为 nil。
func (d *DB) ConsoleInterceptsPage(page, size int, filter InterceptApprovalFilter) ([]InterceptApprovalRow, int, map[string]int, []ConsoleToolCount, error) {
	page, size = clampInterceptPage(page, size)

	// total:全部过滤生效、未分页。
	where, args := interceptApprovalConds("", filter, true, true)
	var total int
	if err := d.QueryRow("SELECT COUNT(*)"+approvalCountFrom+where, args...).Scan(&total); err != nil {
		return nil, 0, nil, nil, err
	}

	offset := (page - 1) * size
	limitArg := len(args) + 1
	offsetArg := limitArg + 1
	dataQ := approvalRowSelect + where +
		" ORDER BY ip.created_at DESC, ip.id DESC LIMIT $" + fmt.Sprint(limitArg) +
		" OFFSET $" + fmt.Sprint(offsetArg)
	args = append(args, size, offset)
	rows, err := d.Query(dataQ, args...)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	defer rows.Close()
	items := []InterceptApprovalRow{}
	for rows.Next() {
		var r InterceptApprovalRow
		if err := scanInterceptApprovalRow(rows, &r); err != nil {
			return nil, 0, nil, nil, err
		}
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, nil, nil, err
	}

	// counts:一条 GROUP BY status 出五档,再补零。列必须 ip.status 限定:
	// approvalCountFrom 带 tasks JOIN,它也有 status 列,裸写是 42702 歧义
	// (冒烟抓到;列名一律带 ip./t./c. 前缀是本文件铁律)。
	countWhere, countArgs := interceptApprovalConds("", filter, false, true)
	countRows, err := d.Query(`SELECT ip.status, COUNT(*)`+approvalCountFrom+countWhere+` GROUP BY ip.status`, countArgs...)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	defer countRows.Close()
	raw := map[string]int{}
	for countRows.Next() {
		var status string
		var n int
		if err := countRows.Scan(&status, &n); err != nil {
			return nil, 0, nil, nil, err
		}
		raw[status] = n
	}
	if err := countRows.Err(); err != nil {
		return nil, 0, nil, nil, err
	}
	counts := CompleteInterceptCounts(raw)

	// tools:受 status/q/task_id/decision_source 影响,不受 tool 影响。
	toolWhere, toolArgs := interceptApprovalConds("", filter, true, false)
	toolRows, err := d.Query(`SELECT ip.tool_name AS tool, COUNT(*) AS count`+
		approvalCountFrom+toolWhere+` GROUP BY ip.tool_name ORDER BY count DESC, ip.tool_name LIMIT 50`, toolArgs...)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	defer toolRows.Close()
	tools := []ConsoleToolCount{}
	for toolRows.Next() {
		var t ConsoleToolCount
		if err := toolRows.Scan(&t.Tool, &t.Count); err != nil {
			return nil, 0, nil, nil, err
		}
		tools = append(tools, t)
	}
	if err := toolRows.Err(); err != nil {
		return nil, 0, nil, nil, err
	}
	return items, total, counts, tools, nil
}
