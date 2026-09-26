package db

// 漏洞库(vulndb_entries / vulndb_pocs):沉淀漏洞情报与利用指引。
//
// 三类分类:0day(未公开/自研发现) / nday(已公开无 CVE 编号) / cve(有 CVE 编号)。
// 与 findings 分表:findings 是「某次任务实测打出来的」,vulndb 是「知识库」——
// 实测成果经自动归类后进本表,SRC 成果页引用本表条目。

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// 分类与状态白名单(不加 DB CHECK,由本层校验)。
const (
	VulnCategoryZeroDay = "0day"
	VulnCategoryNDay    = "nday"
	VulnCategoryCVE     = "cve"
)

// ValidVulnCategory 校验分类取值。
func ValidVulnCategory(c string) bool {
	switch c {
	case VulnCategoryZeroDay, VulnCategoryNDay, VulnCategoryCVE:
		return true
	}
	return false
}

// VulnEntry 是漏洞库的一条条目。
type VulnEntry struct {
	ID           int64           `json:"id"`
	Category     string          `json:"category"` // 0day | nday | cve
	Title        string          `json:"title"`
	CVEID        string          `json:"cve_id,omitempty"`
	Vendor       string          `json:"vendor"`
	Product      string          `json:"product"`
	Affected     string          `json:"affected"`
	Severity     string          `json:"severity"`
	VulnClass    string          `json:"vuln_class"`
	Description  string          `json:"description,omitempty"`
	Exploitation string          `json:"exploitation,omitempty"`
	Remediation  string          `json:"remediation,omitempty"`
	Source       string          `json:"source"` // manual | ai_finding | cve_feed
	TaskID       *int64          `json:"task_id,omitempty"`
	FindingID    *int64          `json:"finding_id,omitempty"`
	Refs         json.RawMessage `json:"refs"`
	Tags         json.RawMessage `json:"tags"`
	PocCount     int             `json:"poc_count"`
	// KEV 标记该 CVE 在 CISA 已知被在野利用目录中(采集器维护,人工编辑不覆盖)。
	KEV       bool      `json:"kev"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// VulnPoc 是漏洞库条目下的一条 PoC。
type VulnPoc struct {
	ID        int64     `json:"id"`
	EntryID   int64     `json:"entry_id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"` // poc | exp | nuclei | script
	Language  string    `json:"language"`
	Content   string    `json:"content,omitempty"`
	Source    string    `json:"source"` // manual | github | exploitdb | ai_generated
	SourceURL string    `json:"source_url"`
	Verified  bool      `json:"verified"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// vulnEntryListCols 列表不带 description/exploitation/remediation(可能很长)。
const vulnEntryListCols = `id, category, title, cve_id, vendor, product, affected, severity,
	vuln_class, source, task_id, finding_id, refs, tags, poc_count, kev, status, created_at, updated_at`

const vulnEntryFullCols = vulnEntryListCols + `, description, exploitation, remediation`

func scanVulnEntry(sc interface{ Scan(...any) error }, full bool) (*VulnEntry, error) {
	var e VulnEntry
	dest := []any{&e.ID, &e.Category, &e.Title, &e.CVEID, &e.Vendor, &e.Product, &e.Affected, &e.Severity,
		&e.VulnClass, &e.Source, &e.TaskID, &e.FindingID, &e.Refs, &e.Tags, &e.PocCount, &e.KEV, &e.Status, &e.CreatedAt, &e.UpdatedAt}
	if full {
		dest = append(dest, &e.Description, &e.Exploitation, &e.Remediation)
	}
	if err := sc.Scan(dest...); err != nil {
		return nil, err
	}
	if e.Refs == nil {
		e.Refs = json.RawMessage("[]")
	}
	if e.Tags == nil {
		e.Tags = json.RawMessage("[]")
	}
	return &e, nil
}

// VulnEntryFilter 是漏洞库列表的筛选条件。空值 = 不限。
type VulnEntryFilter struct {
	Category string // 0day | nday | cve(空=全部)
	// Categories 是多分类筛选(如漏洞库页要 0day+Nday 但不要 cve)。非空时优先于 Category。
	// 后端支持它而非让前端过滤:分页必须按过滤后的集合算,否则每页条数会不齐。
	Categories []string
	Severity   string // critical | high | medium | low
	Status     string // active | archived | draft
	Source     string // manual | ai_finding | cve_feed
	Q          string // 标题/描述/利用指引/厂商/产品 关键字
	TaskID     int64  // 按来源任务筛选(0=不限)
	HasPoc     string // "1"=仅看有 PoC,"0"=仅看无 PoC,空=不限
	Kev        string // "1"=仅在野利用(KEV),"0"=仅非 KEV,空=不限
	Sort       string // updated | created | severity | poc(缺省 updated)
	Desc       bool
	Limit      int
	Offset     int
}

// vulnEntryFilterWhere 拼装条目列表的公共 WHERE(不含分类条件:分类由调用方按
// 「计数不受分类影响、列表受分类影响」的约定追加)。skipSeverity 供严重度分类
// 计数用——与分类计数同一约定:受其它筛选影响,不受该分类维度自身影响。
func vulnEntryFilterWhere(f VulnEntryFilter, skipSeverity bool) (string, []any) {
	where := []string{"1=1"}
	args := []any{}
	add := func(cond string, v any) {
		where = append(where, fmt.Sprintf(cond, len(args)+1))
		args = append(args, v)
	}
	if f.Q != "" {
		// 标题/描述/利用指引/厂商/产品/CVE 编号 全文匹配。
		where = append(where, fmt.Sprintf(
			`(title ILIKE $%d OR description ILIKE $%d OR exploitation ILIKE $%d
			  OR vendor ILIKE $%d OR product ILIKE $%d OR cve_id ILIKE $%d OR vuln_class ILIKE $%d)`,
			len(args)+1, len(args)+1, len(args)+1, len(args)+1, len(args)+1, len(args)+1, len(args)+1))
		args = append(args, "%"+f.Q+"%")
	}
	if f.Severity != "" && !skipSeverity {
		add("severity = $%d", f.Severity)
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.Source != "" {
		add("source = $%d", f.Source)
	}
	if f.TaskID > 0 {
		add("task_id = $%d", f.TaskID)
	}
	if f.HasPoc == "1" {
		where = append(where, "poc_count > 0")
	} else if f.HasPoc == "0" {
		where = append(where, "poc_count = 0")
	}
	if f.Kev == "1" {
		where = append(where, "kev")
	} else if f.Kev == "0" {
		where = append(where, "NOT kev")
	}
	return strings.Join(where, " AND "), args
}

// ListVulnEntries 分页检索漏洞库条目,并返回分类计数(受 Q 与其它筛选影响,
// 不受 Category 影响,供分类页签展示)。
func (d *DB) ListVulnEntries(f VulnEntryFilter) ([]*VulnEntry, int, map[string]int, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	kw, args := vulnEntryFilterWhere(f, false)

	// 分类计数:只受 Q/其它筛选影响,不受 Category 影响(页签要显示各类总数)。
	counts := map[string]int{}
	rows, err := d.Query(`SELECT category, COUNT(*) FROM vulndb_entries WHERE `+kw+` GROUP BY category`, args...)
	if err != nil {
		return nil, 0, nil, err
	}
	for rows.Next() {
		var c string
		var n int
		if err := rows.Scan(&c, &n); err != nil {
			rows.Close()
			return nil, 0, nil, err
		}
		counts[c] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, nil, err
	}

	filtered := kw
	fargs := append([]any(nil), args...)
	// 分类过滤:Categories(多值)优先于 Category(单值)。两者都作用于列表与总数,
	// 保证分页按过滤后的集合计算。
	cats := f.Categories
	if len(cats) == 0 && f.Category != "" {
		cats = []string{f.Category}
	}
	if len(cats) > 0 {
		ph := make([]string, 0, len(cats))
		for _, c := range cats {
			fargs = append(fargs, c)
			ph = append(ph, fmt.Sprintf("$%d", len(fargs)))
		}
		filtered += " AND category IN (" + strings.Join(ph, ",") + ")"
	}

	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM vulndb_entries WHERE `+filtered, fargs...).Scan(&total); err != nil {
		return nil, 0, nil, err
	}

	order := "updated_at DESC, id DESC"
	switch f.Sort {
	case "created":
		order = "created_at DESC, id DESC"
	case "severity":
		// 严重度按语义排序(critical 最前),同级别按更新时间。
		order = `CASE severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END ASC, updated_at DESC`
	case "poc":
		order = "poc_count DESC, updated_at DESC"
	}
	if f.Desc == false && f.Sort != "" {
		// 显式升序请求(默认降序)。
		order = strings.Replace(order, " DESC", " ASC", 1)
	}

	q := `SELECT ` + vulnEntryListCols + ` FROM vulndb_entries WHERE ` + filtered +
		` ORDER BY ` + order + fmt.Sprintf(` LIMIT $%d OFFSET $%d`, len(fargs)+1, len(fargs)+2)
	fargs = append(fargs, f.Limit, f.Offset)
	items, err := d.Query(q, fargs...)
	if err != nil {
		return nil, 0, nil, err
	}
	defer items.Close()
	out := []*VulnEntry{}
	for items.Next() {
		e, err := scanVulnEntry(items, false)
		if err != nil {
			return nil, 0, nil, err
		}
		out = append(out, e)
	}
	return out, total, counts, items.Err()
}

// GetVulnEntry 读取一条漏洞库条目全文(含描述/利用/修复)。
func (d *DB) GetVulnEntry(id int64) (*VulnEntry, error) {
	e, err := scanVulnEntry(d.QueryRow(`SELECT `+vulnEntryFullCols+` FROM vulndb_entries WHERE id=$1`, id), true)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

// VulnEntryInput 是新建/更新条目的输入。
type VulnEntryInput struct {
	Category     string
	Title        string
	CVEID        string
	Vendor       string
	Product      string
	Affected     string
	Severity     string
	VulnClass    string
	Description  string
	Exploitation string
	Remediation  string
	Source       string
	TaskID       *int64
	FindingID    *int64
	Refs         json.RawMessage
	Tags         json.RawMessage
	Status       string
}

// CreateVulnEntry 新建一条漏洞库条目。
func (d *DB) CreateVulnEntry(in VulnEntryInput) (*VulnEntry, error) {
	if !ValidVulnCategory(in.Category) {
		return nil, fmt.Errorf("无效分类: %q", in.Category)
	}
	if strings.TrimSpace(in.Title) == "" {
		return nil, fmt.Errorf("标题必填")
	}
	if in.Source == "" {
		in.Source = "manual"
	}
	if in.Status == "" {
		in.Status = "active"
	}
	if in.Refs == nil {
		in.Refs = json.RawMessage("[]")
	}
	if in.Tags == nil {
		in.Tags = json.RawMessage("[]")
	}
	// CVE 分类必须有编号(唯一索引靠它去重);非 CVE 分类清空编号避免误占用唯一键。
	cve := strings.ToUpper(strings.TrimSpace(in.CVEID))
	if in.Category != VulnCategoryCVE {
		cve = ""
	}
	e, err := scanVulnEntry(d.QueryRow(`
INSERT INTO vulndb_entries(category, title, cve_id, vendor, product, affected, severity, vuln_class,
	description, exploitation, remediation, source, task_id, finding_id, refs, tags, status)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
RETURNING `+vulnEntryFullCols,
		in.Category, strings.TrimSpace(in.Title), cve, in.Vendor, in.Product, in.Affected, in.Severity,
		in.VulnClass, in.Description, in.Exploitation, in.Remediation, in.Source,
		in.TaskID, in.FindingID, in.Refs, in.Tags, in.Status), true)
	if err != nil {
		return nil, err
	}
	return e, nil
}

// UpdateVulnEntry 更新条目(全字段覆盖,调用方先读再改)。
func (d *DB) UpdateVulnEntry(id int64, in VulnEntryInput) error {
	if !ValidVulnCategory(in.Category) {
		return fmt.Errorf("无效分类: %q", in.Category)
	}
	if strings.TrimSpace(in.Title) == "" {
		return fmt.Errorf("标题必填")
	}
	cve := strings.ToUpper(strings.TrimSpace(in.CVEID))
	if in.Category != VulnCategoryCVE {
		cve = ""
	}
	if in.Refs == nil {
		in.Refs = json.RawMessage("[]")
	}
	if in.Tags == nil {
		in.Tags = json.RawMessage("[]")
	}
	if in.Status == "" {
		in.Status = "active"
	}
	_, err := d.Exec(`UPDATE vulndb_entries SET
	category=$2, title=$3, cve_id=$4, vendor=$5, product=$6, affected=$7, severity=$8, vuln_class=$9,
	description=$10, exploitation=$11, remediation=$12, refs=$13, tags=$14, status=$15, updated_at=now()
WHERE id=$1`,
		id, in.Category, strings.TrimSpace(in.Title), cve, in.Vendor, in.Product, in.Affected, in.Severity,
		in.VulnClass, in.Description, in.Exploitation, in.Remediation, in.Refs, in.Tags, in.Status)
	return err
}

// DeleteVulnEntry 删除条目(PoC 级联删除)。
func (d *DB) DeleteVulnEntry(id int64) (bool, error) {
	res, err := d.Exec(`DELETE FROM vulndb_entries WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ---- PoC ----

// ListVulnPocs 列出某条目的全部 PoC(正文只在需要时读,列表可带 content=false)。
func (d *DB) ListVulnPocs(entryID int64, withContent bool) ([]*VulnPoc, error) {
	cols := `id, entry_id, name, kind, language, source, source_url, verified, created_at, updated_at`
	if withContent {
		cols = `id, entry_id, name, kind, language, source, source_url, verified, created_at, updated_at, content`
	}
	rows, err := d.Query(`SELECT `+cols+` FROM vulndb_pocs WHERE entry_id=$1 ORDER BY id`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*VulnPoc{}
	for rows.Next() {
		var p VulnPoc
		dest := []any{&p.ID, &p.EntryID, &p.Name, &p.Kind, &p.Language, &p.Source, &p.SourceURL, &p.Verified, &p.CreatedAt, &p.UpdatedAt}
		if withContent {
			dest = append(dest, &p.Content)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

// GetVulnPoc 读取单条 PoC 全文。
func (d *DB) GetVulnPoc(id int64) (*VulnPoc, error) {
	var p VulnPoc
	err := d.QueryRow(`SELECT id, entry_id, name, kind, language, source, source_url, verified,
		created_at, updated_at, content FROM vulndb_pocs WHERE id=$1`, id).
		Scan(&p.ID, &p.EntryID, &p.Name, &p.Kind, &p.Language, &p.Source, &p.SourceURL, &p.Verified,
			&p.CreatedAt, &p.UpdatedAt, &p.Content)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// VulnPocInput 是新建/更新 PoC 的输入。
type VulnPocInput struct {
	EntryID   int64
	Name      string
	Kind      string
	Language  string
	Content   string
	Source    string
	SourceURL string
	Verified  bool
}

// CreateVulnPoc 新增一条 PoC 并刷新条目的 poc_count。
// 同 entry + source_url 已存在时返回 (0, false, nil) 表示跳过(自动发现去重)。
func (d *DB) CreateVulnPoc(in VulnPocInput) (*VulnPoc, bool, error) {
	if in.EntryID <= 0 {
		return nil, false, fmt.Errorf("entry_id 必填")
	}
	if in.Kind == "" {
		in.Kind = "poc"
	}
	if in.Source == "" {
		in.Source = "manual"
	}
	if strings.TrimSpace(in.Name) == "" {
		in.Name = in.Kind
	}
	var p VulnPoc
	err := d.QueryRow(`
INSERT INTO vulndb_pocs(entry_id, name, kind, language, content, source, source_url, verified)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (entry_id, source_url) WHERE source_url <> '' DO NOTHING
RETURNING id, entry_id, name, kind, language, source, source_url, verified, created_at, updated_at`,
		in.EntryID, strings.TrimSpace(in.Name), in.Kind, in.Language, in.Content, in.Source, in.SourceURL, in.Verified).
		Scan(&p.ID, &p.EntryID, &p.Name, &p.Kind, &p.Language, &p.Source, &p.SourceURL, &p.Verified, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, false, nil // 同来源 URL 已存在,跳过
	}
	if err != nil {
		return nil, false, err
	}
	if err := d.refreshPocCount(in.EntryID); err != nil {
		return nil, false, err
	}
	return &p, true, nil
}

// UpdateVulnPoc 更新 PoC。
func (d *DB) UpdateVulnPoc(id int64, in VulnPocInput) error {
	if in.Kind == "" {
		in.Kind = "poc"
	}
	_, err := d.Exec(`UPDATE vulndb_pocs SET
	name=$2, kind=$3, language=$4, content=$5, verified=$6, updated_at=now() WHERE id=$1`,
		id, strings.TrimSpace(in.Name), in.Kind, in.Language, in.Content, in.Verified)
	return err
}

// DeleteVulnPoc 删除 PoC 并刷新计数。
func (d *DB) DeleteVulnPoc(id int64) (bool, error) {
	var entryID int64
	if err := d.QueryRow(`DELETE FROM vulndb_pocs WHERE id=$1 RETURNING entry_id`, id).Scan(&entryID); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return true, d.refreshPocCount(entryID)
}

func (d *DB) refreshPocCount(entryID int64) error {
	_, err := d.Exec(`UPDATE vulndb_entries SET poc_count =
		(SELECT COUNT(*) FROM vulndb_pocs WHERE entry_id=$1) WHERE id=$1`, entryID)
	return err
}

// UpsertCVE 按 CVE 编号 upsert 一条漏洞情报(定时搜集器用)。
// 已存在时只更新情报字段,不覆盖人工编辑的利用指引/修复建议。
func (d *DB) UpsertCVE(cveID, title, description, severity, vendor, product, affected string, refs json.RawMessage) (id int64, inserted bool, err error) {
	cveID = strings.ToUpper(strings.TrimSpace(cveID))
	if cveID == "" {
		return 0, false, fmt.Errorf("cve_id 必填")
	}
	if refs == nil {
		refs = json.RawMessage("[]")
	}
	// refs 用合并而非覆盖:文章/RSS 源先补进去的参考链接不能被下一轮 NVD 拉取冲掉。
	// ⚠️ 这条注释必须留在 Go 层——曾把 `//` 注释写进下面的 SQL 字符串里,导致
	// PostgreSQL 对所有调用报 42601(语法错误在 "//" 附近),且常规轮/回填同时
	// 静默失败(2026-09-26 冒烟抓到)。SQL 字符串里只能用 `--` 注释。
	err = d.QueryRow(`
INSERT INTO vulndb_entries(category, title, cve_id, description, severity, vendor, product, affected, refs, source)
VALUES ('cve', $1, $2, $3, $4, $5, $6, $7, $8, 'cve_feed')
ON CONFLICT (cve_id) WHERE cve_id <> '' DO UPDATE SET
	title = CASE WHEN EXCLUDED.title <> '' THEN EXCLUDED.title ELSE vulndb_entries.title END,
	description = CASE WHEN EXCLUDED.description <> '' THEN EXCLUDED.description ELSE vulndb_entries.description END,
	severity = CASE WHEN EXCLUDED.severity <> '' THEN EXCLUDED.severity ELSE vulndb_entries.severity END,
	vendor = CASE WHEN EXCLUDED.vendor <> '' THEN EXCLUDED.vendor ELSE vulndb_entries.vendor END,
	product = CASE WHEN EXCLUDED.product <> '' THEN EXCLUDED.product ELSE vulndb_entries.product END,
	affected = CASE WHEN EXCLUDED.affected <> '' THEN EXCLUDED.affected ELSE vulndb_entries.affected END,
	refs = (SELECT COALESCE(jsonb_agg(DISTINCT x), '[]'::jsonb)
		FROM jsonb_array_elements(vulndb_entries.refs || EXCLUDED.refs) AS x),
	updated_at = now()
RETURNING id, (xmax = 0) AS inserted`,
		title, cveID, description, severity, vendor, product, affected, refs).Scan(&id, &inserted)
	return id, inserted, err
}

// FindVulnEntryByCVE 按 CVE 编号查条目(自动发现 PoC 时定位目标条目)。
func (d *DB) FindVulnEntryByCVE(cveID string) (*VulnEntry, error) {
	cveID = strings.ToUpper(strings.TrimSpace(cveID))
	if cveID == "" {
		return nil, nil
	}
	e, err := scanVulnEntry(d.QueryRow(`SELECT `+vulnEntryListCols+` FROM vulndb_entries WHERE cve_id=$1`, cveID), false)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

// FindVulnEntryByFinding 按来源发现查已入库的条目(自动归类幂等判定用)。
func (d *DB) FindVulnEntryByFinding(findingID int64) (*VulnEntry, error) {
	if findingID <= 0 {
		return nil, nil
	}
	e, err := scanVulnEntry(d.QueryRow(`SELECT `+vulnEntryListCols+` FROM vulndb_entries
		WHERE finding_id=$1 LIMIT 1`, findingID), false)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

// ---- 采集运行记录 ----

// VulnSyncRun 是一轮采集记录。
type VulnSyncRun struct {
	ID         int64      `json:"id"`
	Source     string     `json:"source"`
	Status     string     `json:"status"`
	Fetched    int        `json:"fetched"`
	Inserted   int        `json:"inserted"`
	Updated    int        `json:"updated"`
	PocsFound  int        `json:"pocs_found"`
	Error      string     `json:"error"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// StartVulnSyncRun 开一轮采集记录,返回 id。
func (d *DB) StartVulnSyncRun(source string) (int64, error) {
	var id int64
	err := d.QueryRow(`INSERT INTO vulndb_sync_runs(source, status) VALUES ($1,'running') RETURNING id`, source).Scan(&id)
	return id, err
}

// FinishVulnSyncRun 结束一轮采集记录。
func (d *DB) FinishVulnSyncRun(id int64, status string, fetched, inserted, updated, pocs int, errMsg string) error {
	_, err := d.Exec(`UPDATE vulndb_sync_runs SET status=$2, fetched=$3, inserted=$4, updated=$5,
		pocs_found=$6, error=$7, finished_at=now() WHERE id=$1`,
		id, status, fetched, inserted, updated, pocs, errMsg)
	return err
}

// ListVulnSyncRuns 最近若干轮采集记录(页面展示同步状态)。
func (d *DB) ListVulnSyncRuns(limit int) ([]*VulnSyncRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := d.Query(`SELECT id, source, status, fetched, inserted, updated, pocs_found, error,
		started_at, finished_at FROM vulndb_sync_runs ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*VulnSyncRun{}
	for rows.Next() {
		var r VulnSyncRun
		var fin sql.NullTime
		if err := rows.Scan(&r.ID, &r.Source, &r.Status, &r.Fetched, &r.Inserted, &r.Updated,
			&r.PocsFound, &r.Error, &r.StartedAt, &fin); err != nil {
			return nil, err
		}
		if fin.Valid {
			t := fin.Time
			r.FinishedAt = &t
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// CountVulnEntriesByCategory 各分类条目总数(漏洞库页签与概览用)。
func (d *DB) CountVulnEntriesByCategory() (map[string]int, int, error) {
	rows, err := d.Query(`SELECT category, COUNT(*) FROM vulndb_entries GROUP BY category`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := map[string]int{}
	total := 0
	for rows.Next() {
		var c string
		var n int
		if err := rows.Scan(&c, &n); err != nil {
			return nil, 0, err
		}
		out[c] = n
		total += n
	}
	return out, total, rows.Err()
}

// ---- v2 采集源支撑(KEV 在野利用 / 文章参考链接合并) ----

// SetKevByCVE 按 CVE 编号设置「在野利用」标记(KEV 采集器专用,幂等)。
// 返回条目是否存在:KEV 目录里可能有本库尚未收录的 CVE(等 NVD 回填入库后,
// 下一轮 KEV 刷新自然补标记),不存在不算错误。
func (d *DB) SetKevByCVE(cveID string, kev bool) (bool, error) {
	cveID = strings.ToUpper(strings.TrimSpace(cveID))
	if cveID == "" {
		return false, nil
	}
	res, err := d.Exec(`UPDATE vulndb_entries SET kev=$2 WHERE cve_id=$1`, cveID, kev)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListKevCveIds 当前已标记在野利用的全部 CVE 编号(KEV 刷新时反向清除已移出目录的条目)。
func (d *DB) ListKevCveIds() ([]string, error) {
	rows, err := d.Query(`SELECT cve_id FROM vulndb_entries WHERE kev AND cve_id <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// MergeVulnRefs 把参考链接并入条目 refs(JSONB 数组去重合并,保序不保证)。
// 条目不存在时不报错(自动发现与人工编辑竞态下静默跳过)。
func (d *DB) MergeVulnRefs(entryID int64, urls []string) error {
	if entryID <= 0 || len(urls) == 0 {
		return nil
	}
	incoming, err := json.Marshal(urls)
	if err != nil {
		return err
	}
	_, err = d.Exec(`UPDATE vulndb_entries SET refs = (
		SELECT COALESCE(jsonb_agg(DISTINCT x), '[]'::jsonb)
		FROM jsonb_array_elements(refs || $2::jsonb) AS x
	), updated_at=now() WHERE id=$1`, entryID, incoming)
	return err
}

// MergeVulnTags 把标签并入条目 tags(JSONB 数组去重合并)。
func (d *DB) MergeVulnTags(entryID int64, tags []string) error {
	if entryID <= 0 || len(tags) == 0 {
		return nil
	}
	incoming, err := json.Marshal(tags)
	if err != nil {
		return err
	}
	_, err = d.Exec(`UPDATE vulndb_entries SET tags = (
		SELECT COALESCE(jsonb_agg(DISTINCT x), '[]'::jsonb)
		FROM jsonb_array_elements(tags || $2::jsonb) AS x
	), updated_at=now() WHERE id=$1`, entryID, incoming)
	return err
}

// CountVulnEntriesBySeverity 各严重度条目数(CVE 页严重度分类页签用)。
// 与分类计数同一约定:受除严重度外的其它筛选影响,不受严重度自身影响;
// Category/Categories 是固定范围(不是页签维度),必须照常生效——否则 CVE 页
// 的严重度计数会混入 nday/0day(2026-09-26 冒烟抓到:全部页签比实际多 9)。
func (d *DB) CountVulnEntriesBySeverity(f VulnEntryFilter) (map[string]int, error) {
	kw, args := vulnEntryFilterWhere(f, true)
	cats := f.Categories
	if len(cats) == 0 && f.Category != "" {
		cats = []string{f.Category}
	}
	if len(cats) > 0 {
		ph := make([]string, 0, len(cats))
		for _, c := range cats {
			args = append(args, c)
			ph = append(ph, fmt.Sprintf("$%d", len(args)))
		}
		kw += " AND category IN (" + strings.Join(ph, ",") + ")"
	}
	rows, err := d.Query(`SELECT severity, COUNT(*) FROM vulndb_entries WHERE `+kw+` GROUP BY severity`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		// 保留空严重度键:Exploit-DB 反向建档的老洞没有 CVSS,前端「全部」
		// 页签的计数要靠对全值求和得到,漏掉空键会数不对。
		out[s] = n
	}
	return out, rows.Err()
}

// ---- 采集记录分页检索(情报源页签) ----

// VulnSyncRunFilter 是采集记录的筛选条件。空值 = 不限。
type VulnSyncRunFilter struct {
	Source string // 精确匹配 source 键(nvd/exploitdb/…,空=全部)
	Status string // running / done / failed(空=全部)
	Q      string // 匹配 source 键或错误信息
	Limit  int
	Offset int
}

// vulnSyncRunFilterWhere 拼装采集记录的公共 WHERE。skipStatus 供状态计数用
// (计数受其它筛选影响、不受状态自身影响)。
func vulnSyncRunFilterWhere(f VulnSyncRunFilter, skipStatus bool) (string, []any) {
	where := []string{"1=1"}
	args := []any{}
	add := func(cond string, v any) {
		where = append(where, fmt.Sprintf(cond, len(args)+1))
		args = append(args, v)
	}
	if f.Source != "" {
		// 支持逗号分隔多值(情报源页签的源分类:漏洞情报/PoC 源/社区文章 各含多个 source 键)。
		srcs := make([]string, 0, 4)
		for _, s := range strings.Split(f.Source, ",") {
			if s = strings.TrimSpace(s); s != "" {
				srcs = append(srcs, s)
			}
		}
		if len(srcs) == 1 {
			add("source = $%d", srcs[0])
		} else if len(srcs) > 1 {
			ph := make([]string, 0, len(srcs))
			for _, s := range srcs {
				args = append(args, s)
				ph = append(ph, fmt.Sprintf("$%d", len(args)))
			}
			where = append(where, "source IN ("+strings.Join(ph, ",")+")")
		}
	}
	if f.Status != "" && !skipStatus {
		add("status = $%d", f.Status)
	}
	if f.Q != "" {
		add(`(source ILIKE $%d OR error ILIKE $%d)`, "%"+f.Q+"%")
	}
	return strings.Join(where, " AND "), args
}

// ListVulnSyncRunsPage 分页检索采集记录,返回总数与状态计数
// (计数受除状态外其它筛选影响,供状态筛选页签展示)。
func (d *DB) ListVulnSyncRunsPage(f VulnSyncRunFilter) ([]*VulnSyncRun, int, map[string]int, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 20
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	kw, args := vulnSyncRunFilterWhere(f, false)

	// 状态计数(页签用):不受状态自身影响。
	skw, sargs := vulnSyncRunFilterWhere(f, true)
	counts := map[string]int{}
	crows, err := d.Query(`SELECT status, COUNT(*) FROM vulndb_sync_runs WHERE `+skw+` GROUP BY status`, sargs...)
	if err != nil {
		return nil, 0, nil, err
	}
	for crows.Next() {
		var s string
		var n int
		if err := crows.Scan(&s, &n); err != nil {
			crows.Close()
			return nil, 0, nil, err
		}
		counts[s] = n
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return nil, 0, nil, err
	}

	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM vulndb_sync_runs WHERE `+kw, args...).Scan(&total); err != nil {
		return nil, 0, nil, err
	}

	q := `SELECT id, source, status, fetched, inserted, updated, pocs_found, error, started_at, finished_at
		FROM vulndb_sync_runs WHERE ` + kw + ` ORDER BY id DESC` +
		fmt.Sprintf(` LIMIT $%d OFFSET $%d`, len(args)+1, len(args)+2)
	args = append(args, f.Limit, f.Offset)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, 0, nil, err
	}
	defer rows.Close()
	out := []*VulnSyncRun{}
	for rows.Next() {
		var r VulnSyncRun
		var fin sql.NullTime
		if err := rows.Scan(&r.ID, &r.Source, &r.Status, &r.Fetched, &r.Inserted, &r.Updated,
			&r.PocsFound, &r.Error, &r.StartedAt, &fin); err != nil {
			return nil, 0, nil, err
		}
		if fin.Valid {
			t := fin.Time
			r.FinishedAt = &t
		}
		out = append(out, &r)
	}
	return out, total, counts, rows.Err()
}

// LatestVulnSyncRuns 每个采集源最近一轮运行(DISTINCT ON),供情报源页签的
// 实时状态表:分页后的 runs 只能看到切片,各源最新状态必须单独取。
func (d *DB) LatestVulnSyncRuns() ([]*VulnSyncRun, error) {
	rows, err := d.Query(`SELECT DISTINCT ON (source)
		id, source, status, fetched, inserted, updated, pocs_found, error, started_at, finished_at
		FROM vulndb_sync_runs ORDER BY source, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*VulnSyncRun{}
	for rows.Next() {
		var r VulnSyncRun
		var fin sql.NullTime
		if err := rows.Scan(&r.ID, &r.Source, &r.Status, &r.Fetched, &r.Inserted, &r.Updated,
			&r.PocsFound, &r.Error, &r.StartedAt, &fin); err != nil {
			return nil, err
		}
		if fin.Valid {
			t := fin.Time
			r.FinishedAt = &t
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}
