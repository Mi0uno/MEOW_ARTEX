package server

// C2 插件板块(契约 C2_CONTRACTS.md §21,三期 Agent G):
//
//	库管理   /api/c2/plugins…                     CRUD(builtin 拒改删 403)
//	运行     POST /api/c2/sessions/{id}/plugins/{pid}/run
//	工具     c2_plugin_list / c2_plugin_run       agent 自主选插件并运行(stage=post)
//
// 运行链路(核心,结果即任务流水):
//   - script:entry 模板按 params_schema 渲染成命令 → 建 c2_tasks(kind='plugin',
//     input JSON={plugin_id,name,params,command})。beacon 对未知 kind 按
//     input.command 走既有 shell 通道执行(三种 beacon 模板同一行为,无需改造)。
//   - binary:blob 经文件通道(transfer+upload 任务)上传到目标既有可写目录
//     (Windows C:\Users\Public\,POSIX /tmp/)→ 建 plugin 任务执行 entry
//     ({{file}} 占位 = 远端落盘路径;params_schema 声明 after_clean bool 时
//     可选附带清理步骤)。上传任务与执行任务按 id 保序,beacon 逐条执行。
//
// 参数注入安全(renderC2PluginCommand,fail-closed):
//   - params 键必须在 params_schema 声明且在 entry 里有 {{param}} 占位,多余/未声明
//     一律 400(白名单,不静默丢弃);
//   - 值只替换对应占位、单遍替换不重扫;替换前消毒:换行/制表符折叠为空格,
//     命令元字符( ; | & $ ` " ' ( ) < > { } 与 NUL)整体剔除,长度 ≤256 字节;
//   - enum 参数值必须命中声明选项(模板内联 {{x:a|b}} 的选项优先),bool 归一 1/0;
//   - 保留名 file/content 不接受用户注入;after_clean 是保留控制参数(不需要占位)。
//   模板本体(entry/content)是平台侧信任内容,不做消毒——防的是「参数值逃逸出
//   占位符变成命令」,不是审查插件作者。
//
// 接线(签名冻结):registerC2PluginRoutes 由 Agent T 在 registerC2Routes 尾部
// 追加一行调用(Handler 阶段,PG 已就绪);播种(内置插件+agent 工具)就在本函数
// 里做,幂等,失败只记日志不阻断路由。
//
// 铁律沿用(契约 §0/§7):禁 os/exec——一切执行发生在 beacon 目标机上,server 只
// 写任务队列;agent 工具 stage=post 门禁 fail-closed;下发命令先渲染、过 c2_exec
// 同款拦截 Match(deny 拒绝 / ask 转人工审批)再入队。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	pgdb "github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// ---- 接线(签名冻结,Agent T 调用) ----

// registerC2PluginRoutes 注册插件库管理与运行路由(路径同属 /api/c2/*;鉴权由
// Handler 的 requireAuth 统一挂载),并幂等播种内置插件与 agent 工具。
func (s *Server) registerC2PluginRoutes(mux *http.ServeMux) {
	// 插件库
	mux.HandleFunc("GET /api/c2/plugins", s.listC2Plugins)
	mux.HandleFunc("POST /api/c2/plugins", s.createC2Plugin)
	mux.HandleFunc("PUT /api/c2/plugins/{id}", s.updateC2Plugin)
	mux.HandleFunc("DELETE /api/c2/plugins/{id}", s.deleteC2Plugin)
	// 会话内运行
	mux.HandleFunc("POST /api/c2/sessions/{id}/plugins/{pid}/run", s.runC2SessionPlugin)

	s.initC2PluginSeeds()
}

// initC2PluginSeeds 幂等播种:内置插件(builtin=true,整行随版本修正)+ 插件
// agent 工具(绑 worker,stage=post,与 c2_agent 工具同一双保险模式)。
func (s *Server) initC2PluginSeeds() {
	if s.m == nil || s.m.PG() == nil {
		return
	}
	if ins, upd, err := s.m.PG().SeedBuiltinC2Plugins(); err != nil {
		log.Printf("[c2-plugin] 内置插件播种失败: %v", err)
	} else if ins+upd > 0 {
		log.Printf("[c2-plugin] 内置插件播种完成(新插 %d,更新 %d)", ins, upd)
	}
	for _, t := range s.c2PluginTools() {
		schema, _ := json.Marshal(t.InputSchema())
		agents, _ := json.Marshal([]string{"worker"})
		if err := s.m.PG().SeedToolWithStage(t.Name(), t.Description(), schema, agents, "post"); err != nil {
			log.Printf("[c2-plugin] seed %s 失败: %v", t.Name(), err)
		}
	}
}

// ---- 参数渲染与注入防护(纯函数,单测覆盖) ----

// 保留参数名:file=binary 插件上传后的远端落盘路径;content=插件脚本正文;
// after_clean=binary 运行后的清理开关(params_schema 声明的 bool 控制参数,
// 不需要出现在 entry 里)。
const (
	c2PluginFileParam    = "file"
	c2PluginBodyParam    = "content"
	c2PluginCleanParam   = "after_clean"
	c2ParamValueMax      = 256      // 单个参数值消毒后的字节上限
	c2ParamCountMax      = 32       // 单次运行的参数个数上限
	c2BinaryUploadLimit  = 16 << 20 // binary 插件 blob 上限(与页面上传口径一致)
	c2PluginBodyMaxBytes = 1 << 20  // binary 插件 content 文本兜底上限(建/编 API 已限 4MB,这里再兜)
)

// c2ParamValueSanitizer 参数值消毒表:换行/制表符折叠为空格(防多命令),命令
// 元字符整体剔除(防逃逸占位符拼命令)。反斜杠保留——Windows 路径是合法取值。
var c2ParamValueSanitizer = strings.NewReplacer(
	"\x00", "", "\r", " ", "\n", " ", "\t", " ",
	";", "", "|", "", "&", "", "$", "", "`", "",
	"\"", "", "'", "", "(", "", ")", "",
	"<", "", ">", "", "{", "", "}", "",
)

// c2PlaceholderRe 匹配 entry 模板占位:{{name}} / {{name:默认}} / {{name:选1|选2}}。
var c2PlaceholderRe = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*(?::([^{}]*))?\}\}`)

// c2ParamKeyRe 合法参数键(防御性:参数键只作白名单比对,不进命令)。
var c2ParamKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// c2ParamSpec 是 params_schema 里单个参数的声明。
type c2ParamSpec struct {
	Type    string   // string|bool|enum(未知类型按 string)
	Default string   // 声明默认值
	Options []string // enum 选项
	Desc    string
}

// parseC2ParamsSchema 解析参数声明;空/坏 schema 归一为空表(渲染时全部走模板
// 内联默认值),永不 nil。
func parseC2ParamsSchema(schema json.RawMessage) map[string]c2ParamSpec {
	out := map[string]c2ParamSpec{}
	if len(schema) == 0 {
		return out
	}
	var raw map[string]map[string]any
	if err := json.Unmarshal(schema, &raw); err != nil {
		return out
	}
	for name, fields := range raw {
		if !c2ParamKeyRe.MatchString(name) {
			continue
		}
		spec := c2ParamSpec{Type: "string"}
		if t, ok := fields["type"].(string); ok && t != "" {
			spec.Type = t
		}
		if d, ok := fields["default"].(string); ok {
			spec.Default = d
		} else if b, ok := fields["default"].(bool); ok {
			if b {
				spec.Default = "1"
			} else {
				spec.Default = "0"
			}
		}
		if opts, ok := fields["options"].([]any); ok {
			for _, o := range opts {
				if s, ok := o.(string); ok {
					spec.Options = append(spec.Options, s)
				}
			}
		}
		if d, ok := fields["desc"].(string); ok {
			spec.Desc = d
		}
		out[name] = spec
	}
	return out
}

// c2InlineSpec 拆模板内联声明 "默认|选1|选2":enum 用全表做选项、首项为默认;
// 其它类型只取首段做默认值。
func c2InlineSpec(spec string) (def string, options []string) {
	parts := strings.Split(spec, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	if parts[0] == "" {
		return "", nil
	}
	return parts[0], parts
}

// c2CheckParamValue 按声明类型校验/归一参数取值,消毒后返回。fail-closed:
// enum 未命中选项、bool 非法取值、超长均报错。
func c2CheckParamValue(name string, spec c2ParamSpec, inlineOptions []string, v string) (string, error) {
	v = c2ParamValueSanitizer.Replace(v)
	if len(v) > c2ParamValueMax {
		return "", fmt.Errorf("参数 %s 超长(消毒后 %d 字节,上限 %d)", name, len(v), c2ParamValueMax)
	}
	switch spec.Type {
	case "bool":
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true":
			return "1", nil
		case "0", "false", "":
			return "0", nil
		default:
			return "", fmt.Errorf("参数 %s 是 bool,只接受 1/0/true/false,实得 %q", name, v)
		}
	case "enum":
		opts := spec.Options
		if len(inlineOptions) > 0 {
			opts = inlineOptions // 模板内联选项优先(自文档化)
		}
		if len(opts) == 0 {
			return "", fmt.Errorf("参数 %s 是 enum 但未声明选项(params_schema options 或模板 {{x:a|b}})", name)
		}
		for _, o := range opts {
			if v == o {
				return v, nil
			}
		}
		return "", fmt.Errorf("参数 %s 取值 %q 不在允许选项 %v 内", name, v, opts)
	default: // string 及未知类型:消毒后的自由文本
		return v, nil
	}
}

// renderC2PluginCommand 把 entry 模板渲染成命令。reserved 是保留占位的取值
// (file/content,信任内容不消毒——消毒会破坏脚本正文;file 为服务端构造路径)。
// 返回 (命令, 生效参数表, 错误);生效参数表含归一后的 after_clean(若声明),
// 供运行链路与流水留档消费。
func renderC2PluginCommand(entry string, schema json.RawMessage, params map[string]string, reserved map[string]string) (string, map[string]string, error) {
	specs := parseC2ParamsSchema(schema)
	if len(params) > c2ParamCountMax {
		return "", nil, fmt.Errorf("参数个数超过上限 %d", c2ParamCountMax)
	}
	// 收集模板里真实出现的占位(同名多处以首个声明为准,保持出现顺序)。
	seen := map[string]bool{}
	type placeholder struct {
		name, spec string
	}
	var order []placeholder
	for _, m := range c2PlaceholderRe.FindAllStringSubmatch(entry, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			order = append(order, placeholder{name: m[1], spec: m[2]})
		}
	}
	// 用户参数白名单三道关:键合法、已声明、模板里有占位(保留名除外)。
	for k := range params {
		if !c2ParamKeyRe.MatchString(k) {
			return "", nil, fmt.Errorf("非法参数名 %q(仅字母/数字/下划线,且不以数字开头)", k)
		}
		switch k {
		case c2PluginFileParam, c2PluginBodyParam:
			return "", nil, fmt.Errorf("参数名 %s 是保留名,不接受外部注入", k)
		case c2PluginCleanParam:
			// 保留控制参数:不进命令,只作运行开关(渲染尾部统一归一);
			// 但同样必须先在 params_schema 里声明。
			if _, ok := specs[k]; !ok {
				return "", nil, fmt.Errorf("参数 %s 未在 params_schema 声明(拒绝未声明参数)", k)
			}
			continue
		}
		if _, ok := specs[k]; !ok {
			return "", nil, fmt.Errorf("参数 %s 未在 params_schema 声明(拒绝未声明参数)", k)
		}
		if !seen[k] {
			return "", nil, fmt.Errorf("参数 %s 在 entry 模板中没有 {{%s}} 占位(拒绝无效参数)", k, k)
		}
	}
	// 占位逐个取值:用户值 → 声明默认 → 模板内联默认,都没有 = 必填缺失。
	effective := map[string]string{}
	values := map[string]string{}
	for _, ph := range order {
		name := ph.name
		if name == c2PluginFileParam || name == c2PluginBodyParam {
			rv, ok := reserved[name]
			if !ok {
				return "", nil, fmt.Errorf("entry 引用了保留占位 {{%s}} 但当前运行形态未提供", name)
			}
			if name == c2PluginBodyParam && rv == "" {
				return "", nil, fmt.Errorf("entry 引用了 {{content}} 但脚本正文(content)为空")
			}
			values[name] = rv
			continue
		}
		spec := specs[name]
		inlineDef, inlineOpts := c2InlineSpec(ph.spec)
		raw, provided := params[name]
		if !provided {
			raw = spec.Default
		}
		if raw == "" {
			raw = inlineDef
		}
		if raw == "" && !provided {
			return "", nil, fmt.Errorf("缺少必填参数 %s%s", name, func() string {
				if spec.Desc != "" {
					return "(" + spec.Desc + ")"
				}
				return ""
			}())
		}
		sv, err := c2CheckParamValue(name, spec, inlineOpts, raw)
		if err != nil {
			return "", nil, err
		}
		effective[name] = sv
		values[name] = sv
	}
	// after_clean 保留控制参数:声明了就归一出生效值(供运行链路判断,不进命令)。
	if spec, ok := specs[c2PluginCleanParam]; ok {
		raw, ok := params[c2PluginCleanParam]
		if !ok {
			raw = spec.Default
		}
		sv, err := c2CheckParamValue(c2PluginCleanParam, c2ParamSpec{Type: "bool"}, nil, raw)
		if err != nil {
			return "", nil, err
		}
		effective[c2PluginCleanParam] = sv
	}
	// 单遍替换:按占位顺序重建字符串,替换进来的值不被重扫(杜绝二次展开)。
	cmd := c2PlaceholderRe.ReplaceAllStringFunc(entry, func(m string) string {
		sub := c2PlaceholderRe.FindStringSubmatch(m)
		if v, ok := values[sub[1]]; ok {
			return v
		}
		return m // 理论不可达(values 覆盖全部占位)
	})
	return cmd, effective, nil
}

// c2AfterCleanEnabled binary 运行是否附带清理:仅当 params_schema 显式声明了
// bool 型 after_clean(用户传值或声明默认命中 1/true)。
func c2AfterCleanEnabled(schema json.RawMessage, params map[string]string) bool {
	spec, ok := parseC2ParamsSchema(schema)[c2PluginCleanParam]
	if !ok || spec.Type != "bool" {
		return false
	}
	raw, ok := params[c2PluginCleanParam]
	if !ok {
		raw = spec.Default
	}
	sv, err := c2CheckParamValue(c2PluginCleanParam, c2ParamSpec{Type: "bool"}, nil, raw)
	return err == nil && sv == "1"
}

// ---- binary 插件:远端路径与清理步骤 ----

// c2PluginFileStem 由插件名派生安全文件名段(只留字母数字与 .-_),
// 供远端落盘命名;空名退回 "plugin"。
func c2PluginFileStem(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := b.String()
	// 防穿越:吃掉前导点与任何 ".." 序列(路径穿越字符不出现在文件名段里)。
	for strings.HasPrefix(s, ".") {
		s = strings.TrimPrefix(s, ".")
	}
	for strings.Contains(s, "..") {
		s = strings.ReplaceAll(s, "..", "_")
	}
	if len(s) > 32 {
		s = s[:32]
	}
	s = strings.TrimRight(s, ".")
	if strings.Contains(s, "..") {
		s = strings.ReplaceAll(s, "..", "_")
	}
	if s == "" {
		return "plugin"
	}
	return s
}

// c2PluginFileExt 按语言给远端文件扩展名(binary 插件多为脚本型工具,语言即
// 运行方式;native 二进制走 sh + chmod 执行,扩展名只作标识)。
func c2PluginFileExt(language string) string {
	switch language {
	case pgdb.C2PluginLanguagePs1:
		return ".ps1"
	case pgdb.C2PluginLanguageSh:
		return ".sh"
	case pgdb.C2PluginLanguagePy:
		return ".py"
	default:
		return ".bin"
	}
}

// c2PluginRemotePath binary 插件在目标机的落盘路径:用目标机既有可写目录
// (beacon 上传直接 Create 文件,不建目录):Windows → C:\Users\Public\,其余 → /tmp/。
func c2PluginRemotePath(sessionOS string, p *pgdb.C2Plugin) string {
	fname := fmt.Sprintf("artex_pl_%d_%s%s", p.ID, c2PluginFileStem(p.Name), c2PluginFileExt(p.Language))
	if strings.Contains(strings.ToLower(sessionOS), "win") {
		return `C:\Users\Public\` + fname
	}
	return "/tmp/" + fname
}

// c2AfterCleanSuffix 清理步骤:删除落盘文件(路径服务端构造,无引号注入面)。
func c2AfterCleanSuffix(sessionOS, remotePath string) string {
	if strings.Contains(strings.ToLower(sessionOS), "win") {
		return `; Remove-Item -LiteralPath '` + remotePath + `' -Force -ErrorAction SilentlyContinue`
	}
	return `; rm -f '` + remotePath + `'`
}

// ---- 运行链路 ----

// c2PluginTaskInput 组任务 input(beacon 约定:未知 kind 取 input.command 走
// shell 通道;plugin_id/name/params 留档,流水页可见人话)。
func c2PluginTaskInput(pluginID int64, name string, params map[string]string, command string, transferID int64) string {
	m := map[string]any{
		"plugin_id": pluginID,
		"name":      name,
		"params":    params,
		"command":   command,
	}
	if transferID > 0 {
		m["transfer_id"] = transferID
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// c2ParamsFromRaw 把请求体里的 params 对象宽容地收敛成 map[string]string
// (字符串原样;数字/布尔转字面文本;数组/对象拒绝)。
func c2ParamsFromRaw(raw json.RawMessage) (map[string]string, error) {
	out := map[string]string{}
	if len(raw) == 0 || string(raw) == "null" {
		return out, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("params 必须是对象: %w", err)
	}
	for k, v := range m {
		var s string
		if json.Unmarshal(v, &s) == nil {
			out[k] = s
			continue
		}
		var n json.Number
		if json.Unmarshal(v, &n) == nil {
			out[k] = n.String()
			continue
		}
		var b bool
		if json.Unmarshal(v, &b) == nil {
			if b {
				out[k] = "true"
			} else {
				out[k] = "false"
			}
			continue
		}
		return nil, fmt.Errorf("参数 %s 的值类型不支持(仅字符串/数字/布尔)", k)
	}
	return out, nil
}

// c2BuildPluginCommand 渲染插件的最终执行命令(script:entry+{{content}};
// binary:entry+{{file}} 远端路径,可选 after_clean 清理尾缀)。渲染纯本地、
// 不触库不触网——调用方先拦截后入队。
func c2BuildPluginCommand(session *pgdb.C2Session, p *pgdb.C2Plugin, params map[string]string) (string, map[string]string, error) {
	if p.Kind == pgdb.C2PluginKindBinary {
		remotePath := c2PluginRemotePath(session.OS, p)
		cmd, effective, err := renderC2PluginCommand(p.Entry, p.ParamsSchema, params, map[string]string{
			c2PluginFileParam: remotePath,
			c2PluginBodyParam: p.Content,
		})
		if err != nil {
			return "", nil, err
		}
		if c2AfterCleanEnabled(p.ParamsSchema, params) {
			cmd += c2AfterCleanSuffix(session.OS, remotePath)
		}
		return cmd, effective, nil
	}
	return renderC2PluginCommand(p.Entry, p.ParamsSchema, params, map[string]string{
		c2PluginBodyParam: p.Content,
	})
}

// c2PluginBlobBytes 取 binary 插件的可上传字节:平台侧 blob 文件优先(必须在
// c2/plugins 之下,防任意读),退回 content 文本;为空/超限报错。
func c2PluginBlobBytes(baseDir string, p *pgdb.C2Plugin) ([]byte, error) {
	var data []byte
	if p.BlobPath != "" {
		if !c2PathUnder(p.BlobPath, c2BlobDir(baseDir, "plugins")) {
			return nil, fmt.Errorf("插件 blob 路径越界,拒绝上传")
		}
		b, err := os.ReadFile(p.BlobPath)
		if err != nil {
			return nil, fmt.Errorf("读插件 blob 失败: %w", err)
		}
		data = b
	}
	if len(data) == 0 && p.Content != "" {
		if len(p.Content) > c2PluginBodyMaxBytes {
			return nil, fmt.Errorf("插件 content 文本 %d 字节,超过上限", len(p.Content))
		}
		data = []byte(p.Content)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("binary 插件缺少可上传内容(blob_path 或 content 至少一项)")
	}
	if len(data) > c2BinaryUploadLimit {
		return nil, fmt.Errorf("binary 内容 %d 字节,超过 %d MB 上限", len(data), c2BinaryUploadLimit>>20)
	}
	return data, nil
}

// c2QueuePluginTasks 按插件形态入队运行链路(command 已由 c2BuildPluginCommand
// 渲染好;binary 先走文件通道上传,再建执行任务,beacon 按 id 保序执行)。
// 返回 (插件任务 id, 传输 id(binary 才有,否则 0))。
func (s *Server) c2QueuePluginTasks(session *pgdb.C2Session, p *pgdb.C2Plugin, effective map[string]string, command string, actor, createdBy string) (int64, int64, error) {
	if p.Kind != pgdb.C2PluginKindBinary {
		taskID, err := s.m.PG().CreateC2PluginTask(session.ID,
			c2PluginTaskInput(p.ID, p.Name, effective, command, 0), actor, createdBy)
		return taskID, 0, err
	}
	data, err := c2PluginBlobBytes(s.m.dir, p)
	if err != nil {
		return 0, 0, err
	}
	remotePath := c2PluginRemotePath(session.OS, p)
	// 1) 传输记录 + 上传任务(blob 分片拉取复用既有文件通道)。
	transferID, err := s.m.PG().CreateC2Transfer(pgdb.C2TransferInput{
		SessionID: session.ID, Direction: pgdb.C2TransferUp,
		RemotePath: remotePath, Size: int64(len(data)),
	})
	if err != nil {
		return 0, 0, err
	}
	failTransfer := func(msg string) error {
		_ = s.m.PG().UpdateC2Transfer(transferID, "error", 0, -1, "", msg)
		return fmt.Errorf("%s", msg)
	}
	uploadTaskID, err := s.m.PG().CreateC2Task(pgdb.C2TaskInput{
		SessionID: session.ID, Kind: pgdb.C2TaskKindUpload,
		Input: c2TransferTaskInput(transferID, remotePath), Actor: actor, CreatedBy: createdBy,
	})
	if err != nil {
		return 0, 0, failTransfer("建上传任务失败: " + err.Error())
	}
	if err := s.m.PG().SetC2TransferTask(transferID, uploadTaskID); err != nil {
		log.Printf("[c2-plugin] 传输 #%d 回挂任务失败: %v", transferID, err)
	}
	// 2) blob 落盘(文件名带传输 id,beacon pull 分片从这里读;路径只由服务端构造)。
	blobPath := filepath.Join(c2BlobDir(s.m.dir, "uploads"), fmt.Sprintf("%d.bin", transferID))
	if err := os.MkdirAll(filepath.Dir(blobPath), 0o755); err != nil {
		return 0, 0, failTransfer("建上传目录失败: " + err.Error())
	}
	if err := os.WriteFile(blobPath, data, 0o600); err != nil {
		return 0, 0, failTransfer("写上传 blob 失败: " + err.Error())
	}
	if err := s.m.PG().UpdateC2Transfer(transferID, "queued", 0, -1, blobPath, ""); err != nil {
		log.Printf("[c2-plugin] 传输 #%d 记录 blob 路径失败: %v", transferID, err)
	}
	// 3) 执行任务(排在上传任务之后,beacon 保序执行)。
	taskID, err := s.m.PG().CreateC2PluginTask(session.ID,
		c2PluginTaskInput(p.ID, p.Name, effective, command, transferID), actor, createdBy)
	if err != nil {
		return 0, transferID, err
	}
	return taskID, transferID, nil
}

// ---- HTTP handlers ----

// c2PluginPayload 是建/编插件的请求体(params_schema 原样 JSON;builtin 不可经
// API 设置,播种是唯一来源;blob_path 不开放——binary 内容走 content 文本)。
type c2PluginPayload struct {
	Name         string          `json:"name"`
	Platform     string          `json:"platform"`
	Kind         string          `json:"kind"`
	Language     string          `json:"language"`
	Entry        string          `json:"entry"`
	Content      string          `json:"content"`
	ParamsSchema json.RawMessage `json:"params_schema"`
	Description  string          `json:"description"`
}

// listC2Plugins GET /api/c2/plugins?platform=&kind=&q=&limit=&offset=
func (s *Server) listC2Plugins(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	q := r.URL.Query()
	items, total, err := s.m.PG().ListC2Plugins(pgdb.C2PluginFilter{
		Platform: strings.TrimSpace(q.Get("platform")),
		Kind:     strings.TrimSpace(q.Get("kind")),
		Q:        strings.TrimSpace(q.Get("q")),
		Limit:    atoiDefault(q.Get("limit"), 0),
		Offset:   atoiDefault(q.Get("offset"), 0),
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"plugins": nonNilPtrSlice(items), "total": total})
}

// createC2Plugin POST /api/c2/plugins → 201 {id}
func (s *Server) createC2Plugin(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	var p c2PluginPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	id, err := s.m.PG().CreateC2Plugin(pgdb.C2PluginInput{
		Name: p.Name, Platform: p.Platform, Kind: p.Kind, Language: p.Language,
		Entry: p.Entry, Content: p.Content, ParamsSchema: p.ParamsSchema, Description: p.Description,
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	log.Printf("[c2-plugin] 插件 «%s»(#%d) 已创建", p.Name, id)
	writeJSON(w, 201, map[string]any{"id": id})
}

// updateC2Plugin PUT /api/c2/plugins/{id} — 局部更新语义(未提供的字段保持原值);
// builtin=true 拒改(403)。
func (s *Server) updateC2Plugin(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	existing, err := s.m.PG().GetC2Plugin(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if existing == nil {
		writeErr(w, 404, "插件不存在")
		return
	}
	if existing.Builtin {
		writeErr(w, 403, "内置插件不可修改(随版本播种维护,如需定制请新建副本)")
		return
	}
	var p c2PluginPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(p.Name) == "" {
		p.Name = existing.Name
	}
	if strings.TrimSpace(p.Platform) == "" {
		p.Platform = existing.Platform
	}
	if strings.TrimSpace(p.Kind) == "" {
		p.Kind = existing.Kind
	}
	if strings.TrimSpace(p.Language) == "" {
		p.Language = existing.Language
	}
	if p.Entry == "" {
		p.Entry = existing.Entry
	}
	if p.Content == "" {
		p.Content = existing.Content
	}
	if len(p.ParamsSchema) == 0 {
		p.ParamsSchema = existing.ParamsSchema
	}
	if strings.TrimSpace(p.Description) == "" {
		p.Description = existing.Description
	}
	ok2, err := s.m.PG().UpdateC2Plugin(id, pgdb.C2PluginInput{
		Name: p.Name, Platform: p.Platform, Kind: p.Kind, Language: p.Language,
		Entry: p.Entry, Content: p.Content, ParamsSchema: p.ParamsSchema, Description: p.Description,
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if !ok2 {
		writeErr(w, 403, "内置插件不可修改")
		return
	}
	fresh, err := s.m.PG().GetC2Plugin(id)
	if err != nil || fresh == nil {
		writeErr(w, 500, "插件已更新但回读失败")
		return
	}
	writeJSON(w, 200, fresh)
}

// deleteC2Plugin DELETE /api/c2/plugins/{id} — builtin=true 拒删(403)。
func (s *Server) deleteC2Plugin(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	existing, err := s.m.PG().GetC2Plugin(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if existing == nil {
		writeErr(w, 404, "插件不存在")
		return
	}
	if existing.Builtin {
		writeErr(w, 403, "内置插件不可删除")
		return
	}
	deleted, err := s.m.PG().DeleteC2Plugin(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !deleted {
		writeErr(w, 404, "插件不存在")
		return
	}
	log.Printf("[c2-plugin] 插件 «%s»(#%d) 已删除", existing.Name, id)
	writeJSON(w, 200, map[string]any{"deleted": true})
}

// runC2SessionPlugin POST /api/c2/sessions/{id}/plugins/{pid}/run — {params}:
// script 渲染命令建 plugin 任务;binary 走文件通道上传后执行 → {task_id}。
// 会话/状态校验照一期(createC2SessionTask:存在性校验,不校 status——离线会话
// 排队等心跳,与 shell 任务同语义);HTTP 直跑不过拦截(与一期 HTTP 下发一致,
// 拦截管控在 agent 工具链路生效)。
func (s *Server) runC2SessionPlugin(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	session := s.c2GetSessionOr404(w, id)
	if session == nil {
		return
	}
	pid, ok := c2PluginPathID(w, r, "pid")
	if !ok {
		return
	}
	p, err := s.m.PG().GetC2Plugin(pid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if p == nil {
		writeErr(w, 404, "插件不存在")
		return
	}
	var req struct {
		Params json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	params, err := c2ParamsFromRaw(req.Params)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	command, effective, err := c2BuildPluginCommand(session, p, params)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	taskID, transferID, err := s.c2QueuePluginTasks(session, p, effective, command, pgdb.C2TaskActorUser, "c2-plugin-ui")
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.c2NotifySession(id)
	log.Printf("[c2-plugin] 插件 «%s»(#%d) 已在会话 #%d 排队运行(任务 #%d)", p.Name, p.ID, id, taskID)
	writeJSON(w, 200, map[string]any{"task_id": taskID, "transfer_id": transferID})
}

// c2PluginPathID 解析路径里的数字 id(泛化 c2PathID 以支持 {pid} 段)。
func c2PluginPathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "无效的 "+name)
		return 0, false
	}
	return id, true
}

// ---- agent 工具 ----

// c2PluginTools 返回插件 agent 工具(经 hostTools 注入候选集,tools 表按绑定
// 过滤:只绑 worker、stage=post)。
func (s *Server) c2PluginTools() []actool.CoreTool {
	return []actool.CoreTool{
		s.toolC2PluginList(),
		s.toolC2PluginRun(),
	}
}

// toolC2PluginList 查插件库(id/name/平台/形态/语言/参数声明/说明)。
func (s *Server) toolC2PluginList() actool.CoreTool {
	return roTool("c2_plugin_list",
		"查询 C2 插件库:每个插件含 id/name/platform(windows|linux|macos|cross)/"+
			"kind(script|binary)/language(ps1|sh|py)/params_schema(运行参数声明:类型/默认/选项)/description。"+
			"先查这里拿到插件 id 与参数声明,再用 c2_plugin_run 在指定会话上运行。",
		objSchema(map[string]any{
			"platform": strParam("可选:按目标平台过滤 windows/linux/macos/cross"),
			"q":        strParam("可选:名称/描述关键字"),
		}),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if msg := s.c2RequirePostStage(ctx, "c2_plugin_list"); msg != "" {
				return actool.Errorf(msg), nil
			}
			if s.m == nil || s.m.PG() == nil {
				return actool.Errorf("数据库未初始化"), nil
			}
			var a struct {
				Platform string `json:"platform"`
				Q        string `json:"q"`
			}
			_ = json.Unmarshal(in, &a)
			items, total, err := s.m.PG().ListC2Plugins(pgdb.C2PluginFilter{
				Platform: strings.TrimSpace(a.Platform),
				Q:        strings.TrimSpace(a.Q),
				Limit:    100,
			})
			if err != nil {
				return actool.Errorf("查询插件失败: " + err.Error()), nil
			}
			out := make([]map[string]any, 0, len(items))
			for _, p := range items {
				var schema any
				_ = json.Unmarshal(p.ParamsSchema, &schema)
				out = append(out, map[string]any{
					"id": p.ID, "name": p.Name, "platform": p.Platform,
					"kind": p.Kind, "language": p.Language,
					"params_schema": schema, "description": p.Description,
					"builtin": p.Builtin,
				})
			}
			return jsonResult(map[string]any{"plugins": out, "total": total})
		})
}

// toolC2PluginRun 在指定会话上运行插件并等结果(c2_exec 同款:stage 门禁 +
// c2_exec 拦截 Match + 轮询终态;插件命令最终走 shell 通道,复用同一套管控)。
func (s *Server) toolC2PluginRun() actool.CoreTool {
	return wrTool("c2_plugin_run",
		"在指定 C2 会话(已上线主机)上运行一个库内插件并等待回传结果。"+
			"先用 c2_plugin_list 查插件:script 插件按 params_schema 渲染 entry 命令直走命令通道;"+
			"binary 插件先经文件通道上传到目标临时目录再执行(entry 的 {{file}} = 远端路径,"+
			"声明了 after_clean 时可选清理)。参数键必须与 params_schema 声明一致,值会做注入消毒。"+
			"仅 post(后渗透)阶段任务可用;渲染后的命令会过平台拦截规则。",
		objSchema(map[string]any{
			"session_id": strParam("会话 id(c2_session_list 返回的 id)"),
			"plugin_id":  strParam("插件 id(c2_plugin_list 返回的 id)"),
			"params": map[string]any{
				"type":        "object",
				"description": "可选:插件参数对象,键/取值按插件的 params_schema(如 {\"verbose\":\"1\"});不传则全用默认值",
			},
			"timeout_sec": strParam("可选:等待结果超时秒数(≤300,默认 60)"),
		}, "session_id", "plugin_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if msg := s.c2RequirePostStage(ctx, "c2_plugin_run"); msg != "" {
				return actool.Errorf(msg), nil
			}
			if s.m == nil || s.m.PG() == nil {
				return actool.Errorf("数据库未初始化"), nil
			}
			var a struct {
				SessionID  json.RawMessage `json:"session_id"`
				PluginID   json.RawMessage `json:"plugin_id"`
				Params     json.RawMessage `json:"params"`
				TimeoutSec int             `json:"timeout_sec"`
			}
			_ = json.Unmarshal(in, &a)
			m, err := s.c2ToolSession(parseProfileID(a.SessionID))
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			pid := parseProfileID(a.PluginID)
			p, err := s.m.PG().GetC2Plugin(pid)
			if err != nil {
				return actool.Errorf("查插件失败: " + err.Error()), nil
			}
			if p == nil {
				return actool.Errorf(fmt.Sprintf("插件 #%d 不存在(可用 c2_plugin_list 查看插件库)", pid)), nil
			}
			params, err := c2ParamsFromRaw(a.Params)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			// 先渲染后拦截再入队(与 c2_exec 的「拦截在落任务之前」同序;渲染
			// 纯本地不触网,binary 的远端路径也在此确定)。
			command, effective, err := c2BuildPluginCommand(m, p, params)
			if err != nil {
				return actool.Errorf("参数渲染失败: " + err.Error()), nil
			}
			if blocked, text := s.c2ExecIntercept(ctx, command); blocked {
				return actool.Errorf(text), nil
			}
			timeoutSec := a.TimeoutSec
			if timeoutSec <= 0 {
				timeoutSec = c2ExecDefaultTimeout
			}
			if timeoutSec > c2ExecMaxTimeoutSec {
				timeoutSec = c2ExecMaxTimeoutSec
			}
			offlineHint := ""
			if m.Status != pgdb.C2SessionStatusOnline {
				offlineHint = fmt.Sprintf("(注意:该会话当前 %s,任务会排队等 beacon 回连后执行)", m.Status)
			}
			taskID, transferID, err := s.c2QueuePluginTasks(m, p, effective, command,
				pgdb.C2TaskActorAgent, "c2_plugin_run")
			if err != nil {
				return actool.Errorf("插件运行排队失败: " + err.Error()), nil
			}
			s.c2NotifySession(m.ID)
			task, err := s.c2WaitTaskTerminal(ctx, taskID, time.Duration(timeoutSec)*time.Second+c2ExecGrace)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("插件任务 #%d 已下发但等待被取消(%v);结果可稍后从任务流水查看", taskID, err)), nil
			}
			if task == nil {
				return actool.Errorf(fmt.Sprintf("插件任务 #%d 状态读取失败", taskID)), nil
			}
			out, truncated := truncateVPSOutput([]byte(task.Output), c2ExecOutputLimit)
			if truncated {
				out += fmt.Sprintf("\n…[输出超过 %d KB 已截断;完整输出可在 C2 详情页任务流水下载]", c2ExecOutputLimit/1024)
			}
			res := map[string]any{
				"task_id": task.ID, "session_id": m.ID, "plugin_id": p.ID,
				"plugin_name": p.Name, "kind": p.Kind, "status": task.Status,
				"output": out, "truncated": truncated, "error": task.Error,
			}
			if transferID > 0 {
				res["transfer_id"] = transferID
			}
			if task.Status != pgdb.C2TaskStatusDone && task.Status != pgdb.C2TaskStatusError {
				res["hint"] = fmt.Sprintf("等待 %d 秒未到终态(当前 %s)%s;beacon 心跳间隔 %d 秒,可稍后重试或查任务流水",
					timeoutSec, task.Status, offlineHint, m.SleepSec)
			} else if offlineHint != "" {
				res["hint"] = "插件在 beacon 回连后执行" + offlineHint
			}
			return jsonResult(res)
		})
}
