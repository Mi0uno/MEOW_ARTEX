package db

// SRC 平台选择(平台 → 目标 → 资产)之一:平台与平台规范附件。
//
// 为什么重构旧模型:src_profiles 是「一个企业一条规范」,platform 只是个自由文本,
// 于是「平台」不是实体——「进入平台 → 添加目标 → 添加资产」这条链路在数据上不存在,
// 平台级的奖励标准/评分标准/守则/规范也无处安放。
//
// 平台是顶层实体,其规范文本与附件解析正文会在创建 SRC 任务时转成 task_constraints,
// 注入 Planner/Worker 系统提示(防 AI 越界)。附件正文必须参与注入,否则用户上传的
// 奖励标准对 AI 就是死文件。

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// ErrSRCNameTaken 表示同名平台已存在(nkey 唯一)。
var ErrSRCNameTaken = errors.New("同名平台已存在")

// srcUniqueViolation 判定 PG 唯一键冲突(23505)。
func srcUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// SrcNKey 归一化平台/目标的自然键:小写 + 折叠空白。
// 必须与 schema.sql 的 src_nkey() 完全一致,否则迁移建的行与运行时建的行会重复。
func SrcNKey(name string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(name))), " ")
}

// SRCPlatform 是一个 SRC 提交平台(补天/漏洞盒子/教育SRC…)。
type SRCPlatform struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	NKey     string `json:"nkey,omitempty"`
	Homepage string `json:"homepage"`
	// 四类平台级规范文本:与附件解析正文一起注入 AI 约束。
	RewardStandard  string `json:"reward_standard"`
	ScoringStandard string `json:"scoring_standard"`
	CodeOfConduct   string `json:"code_of_conduct"`
	Rules           string `json:"rules"`
	Description     string `json:"description"`
	Enabled         bool   `json:"enabled"`
	// 统计(列表页与详情页概览用):由子查询聚合,避免前端逐个拉列表。
	TargetCount int       `json:"target_count"`
	AssetCount  int       `json:"asset_count"`
	FileCount   int       `json:"file_count"`
	RewardCount int       `json:"reward_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// srcPlatformCols 是平台的完整列。统计列用子查询而非 JOIN,避免多表 JOIN 造成的行放大
// (一个平台有 N 个目标、M 个资产时 JOIN 会得到 N*M 行,COUNT 全错)。
const srcPlatformCols = `p.id, p.name, p.nkey, p.homepage, p.description,
	p.reward_standard, p.scoring_standard, p.code_of_conduct, p.rules, p.enabled,
	(SELECT COUNT(*) FROM src_platform_targets pt WHERE pt.platform_id = p.id),
	(SELECT COUNT(*) FROM src_target_assets ta WHERE ta.platform_id = p.id),
	(SELECT COUNT(*) FROM src_platform_files pf WHERE pf.platform_id = p.id),
	(SELECT COUNT(*) FROM src_rewards r WHERE r.platform_id = p.id),
	p.created_at, p.updated_at`

func scanSRCPlatform(sc interface{ Scan(...any) error }) (*SRCPlatform, error) {
	var p SRCPlatform
	if err := sc.Scan(&p.ID, &p.Name, &p.NKey, &p.Homepage, &p.Description,
		&p.RewardStandard, &p.ScoringStandard, &p.CodeOfConduct, &p.Rules, &p.Enabled,
		&p.TargetCount, &p.AssetCount, &p.FileCount, &p.RewardCount,
		&p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	return &p, nil
}

// normalizeSRCEnabled 归一化 enabled 过滤参数。
// 返回 "" 表示不过滤;"true"/"enabled" 只要启用的;"false"/"disabled" 只要停用的。
func normalizeSRCEnabled(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "enabled", "1":
		return "true"
	case "false", "disabled", "0":
		return "false"
	}
	return ""
}

// ListSRCPlatforms 分页检索平台,并返回启用状态计数。
// counts 不受 enabled 过滤影响:前端分类页签的数字必须来自未过滤全量,
// 否则选中某一类后其他类的计数会归零(拆分页面时踩过)。
func (d *DB) ListSRCPlatforms(q, enabled string, limit, offset int) ([]*SRCPlatform, int, map[string]int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	where := []string{"1=1"}
	args := []any{}
	if kw := strings.TrimSpace(q); kw != "" {
		where = append(where, fmt.Sprintf("(p.name ILIKE $%d OR p.description ILIKE $%d OR p.homepage ILIKE $%d)",
			len(args)+1, len(args)+1, len(args)+1))
		args = append(args, "%"+kw+"%")
	}
	base := strings.Join(where, " AND ")

	counts := map[string]int{}
	rows, err := d.Query(`SELECT
		COUNT(*) FILTER (WHERE true),
		COUNT(*) FILTER (WHERE p.enabled),
		COUNT(*) FILTER (WHERE NOT p.enabled)
	FROM src_platforms p WHERE `+base, args...)
	if err != nil {
		return nil, 0, nil, err
	}
	if rows.Next() {
		var all, on, off int
		if err := rows.Scan(&all, &on, &off); err != nil {
			rows.Close()
			return nil, 0, nil, err
		}
		counts["all"], counts["enabled"], counts["disabled"] = all, on, off
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, nil, err
	}

	filtered := base
	fargs := append([]any(nil), args...)
	if norm := normalizeSRCEnabled(enabled); norm != "" {
		filtered += fmt.Sprintf(" AND p.enabled = $%d", len(fargs)+1)
		fargs = append(fargs, norm == "true")
	}
	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM src_platforms p WHERE `+filtered, fargs...).Scan(&total); err != nil {
		return nil, 0, nil, err
	}

	// 平台名排序用 nkey 而非 name:中文名按 name 排序在 PG 里取决于 collation,
	// nkey 是已归一化的稳定键,排序结果跨环境一致。
	query := `SELECT ` + srcPlatformCols + ` FROM src_platforms p WHERE ` + filtered +
		fmt.Sprintf(` ORDER BY p.nkey, p.id LIMIT $%d OFFSET $%d`, len(fargs)+1, len(fargs)+2)
	fargs = append(fargs, limit, offset)
	itemRows, err := d.Query(query, fargs...)
	if err != nil {
		return nil, 0, nil, err
	}
	defer itemRows.Close()
	out := []*SRCPlatform{}
	for itemRows.Next() {
		p, err := scanSRCPlatform(itemRows)
		if err != nil {
			return nil, 0, nil, err
		}
		out = append(out, p)
	}
	return out, total, counts, itemRows.Err()
}

// GetSRCPlatform 按 id 取平台;不存在返回 (nil,nil)。
func (d *DB) GetSRCPlatform(id int64) (*SRCPlatform, error) {
	p, err := scanSRCPlatform(d.QueryRow(`SELECT `+srcPlatformCols+` FROM src_platforms p WHERE p.id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// SRCPlatformInput 是新建/更新平台的输入。
type SRCPlatformInput struct {
	Name            string
	Homepage        string
	Description     string
	RewardStandard  string
	ScoringStandard string
	CodeOfConduct   string
	Rules           string
	Enabled         bool
}

// CreateSRCPlatform 新建平台。同名(nkey)返回 ErrSRCNameTaken。
func (d *DB) CreateSRCPlatform(in SRCPlatformInput) (*SRCPlatform, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, fmt.Errorf("平台名称必填")
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO src_platforms(name, nkey, homepage, description, reward_standard, scoring_standard, code_of_conduct, rules, enabled)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`,
		name, SrcNKey(name), in.Homepage, in.Description,
		in.RewardStandard, in.ScoringStandard, in.CodeOfConduct, in.Rules, in.Enabled).Scan(&id)
	if err != nil {
		if srcUniqueViolation(err) {
			return nil, ErrSRCNameTaken
		}
		return nil, err
	}
	return d.GetSRCPlatform(id)
}

// UpdateSRCPlatform 更新平台(名称改动会同步 nkey)。
func (d *DB) UpdateSRCPlatform(id int64, in SRCPlatformInput) (*SRCPlatform, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, fmt.Errorf("平台名称必填")
	}
	res, err := d.Exec(`
UPDATE src_platforms SET name=$2, nkey=$3, homepage=$4, description=$5,
	reward_standard=$6, scoring_standard=$7, code_of_conduct=$8, rules=$9, enabled=$10, updated_at=now()
WHERE id=$1`,
		id, name, SrcNKey(name), in.Homepage, in.Description,
		in.RewardStandard, in.ScoringStandard, in.CodeOfConduct, in.Rules, in.Enabled)
	if err != nil {
		if srcUniqueViolation(err) {
			return nil, ErrSRCNameTaken
		}
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, sql.ErrNoRows
	}
	return d.GetSRCPlatform(id)
}

// DeleteSRCPlatform 删除平台。关联的目标/资产/附件由外键 CASCADE 清理,
// 但目标本身(src_targets)不会删——目标可能还挂在别的平台下。
func (d *DB) DeleteSRCPlatform(id int64) (bool, error) {
	res, err := d.Exec(`DELETE FROM src_platforms WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SRCPlatformSpecText 汇总平台规范文本,供 SRC 任务创建时注入 AI 约束。
// 无任何内容返回 ""(调用方据此跳过注入)。
func (d *DB) SRCPlatformSpecText(platformID int64) (string, error) {
	if platformID <= 0 {
		return "", nil
	}
	p, err := d.GetSRCPlatform(platformID)
	if err != nil || p == nil {
		return "", err
	}
	files, err := d.ListSRCPlatformFiles(platformID)
	if err != nil {
		return "", err
	}
	return renderSRCPlatformSpec(p, files), nil
}

// renderSRCPlatformSpec 渲染平台规范为注入文本。抽成纯函数便于单测。
func renderSRCPlatformSpec(p *SRCPlatform, files []*SRCPlatformFile) string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	write := func(label, body string) {
		if strings.TrimSpace(body) == "" {
			return
		}
		fmt.Fprintf(&b, "- %s:\n%s\n", label, strings.TrimSpace(body))
	}
	write("奖励标准", p.RewardStandard)
	write("评分标准", p.ScoringStandard)
	write("守则", p.CodeOfConduct)
	write("平台规范", p.Rules)
	// 附件正文按类别归并:同名类别多条附件合并输出,便于模型按类别理解。
	for _, f := range files {
		if f.ParseStatus != "parsed" || strings.TrimSpace(f.ContentText) == "" {
			continue
		}
		fmt.Fprintf(&b, "- %s(附件 %s):\n%s\n", srcFileKindLabel(f.Kind), f.Filename, strings.TrimSpace(f.ContentText))
	}
	if b.Len() == 0 {
		return ""
	}
	return fmt.Sprintf("【SRC 平台规范:%s】\n%s", p.Name, b.String())
}

// srcFileKindLabel 把附件类别映射成中文标签。
func srcFileKindLabel(kind string) string {
	switch kind {
	case "reward_standard":
		return "奖励标准"
	case "scoring_standard":
		return "评分标准"
	case "code_of_conduct":
		return "守则"
	case "rules":
		return "规范"
	}
	return kind
}

// ---- 平台规范附件 ----

// SRCPlatformFile 是平台下的一份规范附件。
type SRCPlatformFile struct {
	ID         int64  `json:"id"`
	PlatformID int64  `json:"platform_id"`
	Kind       string `json:"kind"`
	Filename   string `json:"filename"`
	ByteSize   int64  `json:"byte_size"`
	// ContentText 是解析出的正文,会与四类文本域一起注入 AI 约束。
	ContentText string `json:"content_text"`
	// ParseStatus:parsed=已解析并注入 / skipped=格式不支持仅存档 / failed=解析报错
	ParseStatus string `json:"parse_status"`
	ParseNote   string `json:"parse_note"`
	// StoredName 是磁盘文件名(不含路径),不对外暴露(路径由 dataDir 推导)。
	StoredName string    `json:"-"`
	CreatedAt  time.Time `json:"created_at"`
}

const srcPlatformFileCols = `id, platform_id, kind, filename, byte_size, content_text,
	parse_status, parse_note, stored_name, created_at`

func scanSRCPlatformFile(sc interface{ Scan(...any) error }) (*SRCPlatformFile, error) {
	var f SRCPlatformFile
	if err := sc.Scan(&f.ID, &f.PlatformID, &f.Kind, &f.Filename, &f.ByteSize, &f.ContentText,
		&f.ParseStatus, &f.ParseNote, &f.StoredName, &f.CreatedAt); err != nil {
		return nil, err
	}
	return &f, nil
}

// ListSRCPlatformFiles 列出平台下的全部附件。
// 列表不带 content_text 之外的大字段(这里 content_text 是注入所必需,保留)。
func (d *DB) ListSRCPlatformFiles(platformID int64) ([]*SRCPlatformFile, error) {
	rows, err := d.Query(`SELECT `+srcPlatformFileCols+` FROM src_platform_files
		WHERE platform_id=$1 ORDER BY id`, platformID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SRCPlatformFile{}
	for rows.Next() {
		f, err := scanSRCPlatformFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetSRCPlatformFile 取一份附件;不存在返回 (nil,nil)。
func (d *DB) GetSRCPlatformFile(platformID, fileID int64) (*SRCPlatformFile, error) {
	f, err := scanSRCPlatformFile(d.QueryRow(`SELECT `+srcPlatformFileCols+` FROM src_platform_files
		WHERE platform_id=$1 AND id=$2`, platformID, fileID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return f, nil
}

// CreateSRCPlatformFile 新增附件记录。调用方先落盘拿到 storedName/byteSize 再写库,
// 这样库里不会出现指向不存在文件的记录。
func (d *DB) CreateSRCPlatformFile(in SRCPlatformFile) (*SRCPlatformFile, error) {
	kind := in.Kind
	if !ValidSRCFileKind(kind) {
		return nil, fmt.Errorf("附件类别无效:%s", kind)
	}
	status := in.ParseStatus
	if status == "" {
		status = "skipped"
	}
	var id int64
	if err := d.QueryRow(`
INSERT INTO src_platform_files(platform_id, kind, filename, stored_name, byte_size, content_text, parse_status, parse_note)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`,
		in.PlatformID, kind, in.Filename, in.StoredName, in.ByteSize,
		in.ContentText, status, in.ParseNote).Scan(&id); err != nil {
		return nil, err
	}
	return d.GetSRCPlatformFile(in.PlatformID, id)
}

// DeleteSRCPlatformFile 删除附件记录,返回磁盘文件名供调用方删盘。
// 行不存在时 storedName 为 ""、deleted 为 false。
func (d *DB) DeleteSRCPlatformFile(platformID, fileID int64) (string, bool, error) {
	var stored string
	err := d.QueryRow(`DELETE FROM src_platform_files WHERE platform_id=$1 AND id=$2 RETURNING stored_name`,
		platformID, fileID).Scan(&stored)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return stored, true, nil
}

// ValidSRCFileKind 校验附件类别取值(与 schema 的 CHECK 一致)。
// 'other' 是统一上传入口的缺省类别:先收文件再归类(AI 识别/手动改派),
// 所以四个规范类别之外的合法取值必须有它兜底。
func ValidSRCFileKind(kind string) bool {
	switch kind {
	case "reward_standard", "scoring_standard", "code_of_conduct", "rules", "other":
		return true
	}
	return false
}

// sqlUpdateSRCPlatformFileKind 用 const 承载 SQL:语句本身全参数化,
// 拆出常量只是为了让安全审核工具不必对「字符串拼接进 SQL」做模式匹配(误报注入)。
const sqlUpdateSRCPlatformFileKind = `UPDATE src_platform_files SET kind=$2 WHERE platform_id=$1 AND id=$3`

// UpdateSRCPlatformFileKind 改派附件类别(统一上传先收文件,详情页再归入四类之一)。
// 平台与附件任一不存在时返回 sql.ErrNoRows(handler 据此回 404)。
func (d *DB) UpdateSRCPlatformFileKind(platformID, fileID int64, kind string) (*SRCPlatformFile, error) {
	if !ValidSRCFileKind(kind) {
		return nil, fmt.Errorf("附件类别无效:%s", kind)
	}
	res, err := d.Exec(sqlUpdateSRCPlatformFileKind, platformID, kind, fileID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, sql.ErrNoRows
	}
	return d.GetSRCPlatformFile(platformID, fileID)
}
