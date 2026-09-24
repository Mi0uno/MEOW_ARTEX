package db

// SRC 平台选择(平台 → 目标 → 资产)之二:目标、平台×目标关联、目标资产。
//
// 两个关键取舍:
//
//  1. 目标是独立表而非 companies 的别名。company_id 可空——平台下可以先只有一个
//     目标名字(还没建企业、还没资产),等资产进来再关联企业。这样「登记一个目标」
//     不会被「必须先建企业」卡住。
//
//  2. 授权范围挂在「平台×目标」关联(src_platform_targets)上,不是挂在目标上。
//     同一家企业可能同时收录在补天和漏洞盒子,两边的授权范围/速率限制/规范完全不同,
//     挂在目标上就无法表达这种差异。所以任何查授权范围的地方都必须同时给 platform_id
//     和 target_id。

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrSRCTargetNameTaken 表示同名目标已存在(nkey 唯一)。
var ErrSRCTargetNameTaken = errors.New("同名目标已存在")

// SRCTarget 是被测试的对象(厂商/学校/单位)。
type SRCTarget struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	CompanyID   *int64 `json:"company_id,omitempty"`
	CompanyName string `json:"company_name,omitempty"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	// 统计:平台数=挂链数;资产/成果跨平台汇总。
	PlatformCount int       `json:"platform_count"`
	AssetCount    int       `json:"asset_count"`
	RewardCount   int       `json:"reward_count"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// srcTargetCols 用子查询聚合统计,避免 JOIN 行放大(同平台列表的理由)。
const srcTargetCols = `t.id, t.name, t.company_id, COALESCE(c.name,''), t.description, t.enabled,
	(SELECT COUNT(*) FROM src_platform_targets pt WHERE pt.target_id = t.id),
	(SELECT COUNT(*) FROM src_target_assets ta WHERE ta.target_id = t.id),
	(SELECT COUNT(*) FROM src_rewards r WHERE r.target_id = t.id),
	t.created_at, t.updated_at`

func scanSRCTarget(sc interface{ Scan(...any) error }) (*SRCTarget, error) {
	var t SRCTarget
	if err := sc.Scan(&t.ID, &t.Name, &t.CompanyID, &t.CompanyName, &t.Description, &t.Enabled,
		&t.PlatformCount, &t.AssetCount, &t.RewardCount, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

// ListSRCTargets 分页检索目标。platformID>0 时只返回挂在该平台下的目标。
func (d *DB) ListSRCTargets(platformID int64, q string, limit, offset int) ([]*SRCTarget, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	where := []string{"1=1"}
	args := []any{}
	if platformID > 0 {
		where = append(where, fmt.Sprintf("EXISTS (SELECT 1 FROM src_platform_targets pt WHERE pt.target_id=t.id AND pt.platform_id=$%d)", len(args)+1))
		args = append(args, platformID)
	}
	if kw := strings.TrimSpace(q); kw != "" {
		where = append(where, fmt.Sprintf("(t.name ILIKE $%d OR t.description ILIKE $%d)", len(args)+1, len(args)+1))
		args = append(args, "%"+kw+"%")
	}
	kw := strings.Join(where, " AND ")
	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM src_targets t WHERE `+kw, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	query := `SELECT ` + srcTargetCols + ` FROM src_targets t
		LEFT JOIN companies c ON c.id=t.company_id WHERE ` + kw +
		fmt.Sprintf(` ORDER BY t.nkey, t.id LIMIT $%d OFFSET $%d`, len(args)+1, len(args)+2)
	args = append(args, limit, offset)
	rows, err := d.Query(query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*SRCTarget{}
	for rows.Next() {
		t, err := scanSRCTarget(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, t)
	}
	return out, total, rows.Err()
}

// GetSRCTarget 按 id 取目标;不存在返回 (nil,nil)。
func (d *DB) GetSRCTarget(id int64) (*SRCTarget, error) {
	t, err := scanSRCTarget(d.QueryRow(`SELECT `+srcTargetCols+` FROM src_targets t
		LEFT JOIN companies c ON c.id=t.company_id WHERE t.id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

// SRCTargetInput 是新建/更新目标的输入。
type SRCTargetInput struct {
	Name        string
	CompanyID   *int64
	Description string
	Enabled     bool
}

// CreateSRCTarget 新建目标。同名(nkey)返回 ErrSRCTargetNameTaken。
func (d *DB) CreateSRCTarget(in SRCTargetInput) (*SRCTarget, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, fmt.Errorf("目标名称必填")
	}
	var companyID any
	if in.CompanyID != nil && *in.CompanyID > 0 {
		companyID = *in.CompanyID
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO src_targets(name, nkey, company_id, description, enabled)
VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		name, SrcNKey(name), companyID, in.Description, in.Enabled).Scan(&id)
	if err != nil {
		if srcUniqueViolation(err) {
			return nil, ErrSRCTargetNameTaken
		}
		return nil, err
	}
	return d.GetSRCTarget(id)
}

// UpdateSRCTarget 更新目标(名称改动会同步 nkey)。
func (d *DB) UpdateSRCTarget(id int64, in SRCTargetInput) (*SRCTarget, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, fmt.Errorf("目标名称必填")
	}
	var companyID any
	if in.CompanyID != nil && *in.CompanyID > 0 {
		companyID = *in.CompanyID
	}
	res, err := d.Exec(`
UPDATE src_targets SET name=$2, nkey=$3, company_id=$4, description=$5, enabled=$6, updated_at=now()
WHERE id=$1`, id, name, SrcNKey(name), companyID, in.Description, in.Enabled)
	if err != nil {
		if srcUniqueViolation(err) {
			return nil, ErrSRCTargetNameTaken
		}
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, sql.ErrNoRows
	}
	return d.GetSRCTarget(id)
}

// DeleteSRCTarget 删除目标。挂链与资产关联由外键 CASCADE 清理。
func (d *DB) DeleteSRCTarget(id int64) (bool, error) {
	res, err := d.Exec(`DELETE FROM src_targets WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ---- 平台×目标关联(授权范围挂这一层) ----

// SRCTargetLink 是「平台×目标」关联,承载该平台下该目标的授权范围与规范。
type SRCTargetLink struct {
	ID           int64  `json:"id"`
	PlatformID   int64  `json:"platform_id"`
	PlatformName string `json:"platform_name,omitempty"`
	TargetID     int64  `json:"target_id"`
	TargetName   string `json:"target_name,omitempty"`
	ScopeNote    string `json:"scope_note"`
	Forbidden    string `json:"forbidden"`
	AllowedTypes string `json:"allowed_types"`
	RateLimit    string `json:"rate_limit"`
	Rules        string `json:"rules"`
	Enabled      bool   `json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

const srcTargetLinkCols = `pt.id, pt.platform_id, COALESCE(p.name,''), pt.target_id, COALESCE(t.name,''),
	pt.scope_note, pt.forbidden, pt.allowed_types, pt.rate_limit, pt.rules, pt.enabled,
	pt.created_at, pt.updated_at`

func scanSRCTargetLink(sc interface{ Scan(...any) error }) (*SRCTargetLink, error) {
	var l SRCTargetLink
	if err := sc.Scan(&l.ID, &l.PlatformID, &l.PlatformName, &l.TargetID, &l.TargetName,
		&l.ScopeNote, &l.Forbidden, &l.AllowedTypes, &l.RateLimit, &l.Rules, &l.Enabled,
		&l.CreatedAt, &l.UpdatedAt); err != nil {
		return nil, err
	}
	return &l, nil
}

// SRCTargetLinkInput 是授权范围与规范的输入。
type SRCTargetLinkInput struct {
	ScopeNote    string
	Forbidden    string
	AllowedTypes string
	RateLimit    string
	Rules        string
	Enabled      bool
}

// ListSRCTargetLinks 列出目标所属的全部平台关联(含各平台下的授权范围)。
func (d *DB) ListSRCTargetLinks(targetID int64) ([]*SRCTargetLink, error) {
	rows, err := d.Query(`SELECT `+srcTargetLinkCols+` FROM src_platform_targets pt
		JOIN src_platforms p ON p.id=pt.platform_id
		JOIN src_targets t   ON t.id=pt.target_id
		WHERE pt.target_id=$1 ORDER BY p.nkey, pt.id`, targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SRCTargetLink{}
	for rows.Next() {
		l, err := scanSRCTargetLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// GetSRCTargetLink 取某平台下的关联;不存在返回 (nil,nil)。
func (d *DB) GetSRCTargetLink(platformID, targetID int64) (*SRCTargetLink, error) {
	l, err := scanSRCTargetLink(d.QueryRow(`SELECT `+srcTargetLinkCols+` FROM src_platform_targets pt
		JOIN src_platforms p ON p.id=pt.platform_id
		JOIN src_targets t   ON t.id=pt.target_id
		WHERE pt.platform_id=$1 AND pt.target_id=$2`, platformID, targetID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return l, nil
}

// LinkSRCTarget 把目标挂到平台下(已存在则更新授权范围,幂等)。
// 用于「在平台下添加已有目标」与「设置授权范围」两条路径。
func (d *DB) LinkSRCTarget(platformID, targetID int64, in SRCTargetLinkInput) (*SRCTargetLink, error) {
	if platformID <= 0 || targetID <= 0 {
		return nil, fmt.Errorf("平台与目标必填")
	}
	if _, err := d.Exec(`
INSERT INTO src_platform_targets(platform_id, target_id, scope_note, forbidden, allowed_types, rate_limit, rules, enabled)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (platform_id, target_id) DO UPDATE SET
	scope_note=EXCLUDED.scope_note, forbidden=EXCLUDED.forbidden,
	allowed_types=EXCLUDED.allowed_types, rate_limit=EXCLUDED.rate_limit,
	rules=EXCLUDED.rules, enabled=EXCLUDED.enabled, updated_at=now()`,
		platformID, targetID, in.ScopeNote, in.Forbidden, in.AllowedTypes, in.RateLimit, in.Rules, in.Enabled); err != nil {
		return nil, err
	}
	return d.GetSRCTargetLink(platformID, targetID)
}

// UpdateSRCTargetLink 只更新授权范围(不动挂链关系)。
func (d *DB) UpdateSRCTargetLink(platformID, targetID int64, in SRCTargetLinkInput) (*SRCTargetLink, error) {
	res, err := d.Exec(`UPDATE src_platform_targets SET
	scope_note=$3, forbidden=$4, allowed_types=$5, rate_limit=$6, rules=$7, enabled=$8, updated_at=now()
WHERE platform_id=$1 AND target_id=$2`,
		platformID, targetID, in.ScopeNote, in.Forbidden, in.AllowedTypes, in.RateLimit, in.Rules, in.Enabled)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, sql.ErrNoRows
	}
	return d.GetSRCTargetLink(platformID, targetID)
}

// UnlinkSRCTarget 解除某平台下的挂链。
// 注意:只删关联行,目标本身与资产关联保留——目标可能还挂在别的平台下。
func (d *DB) UnlinkSRCTarget(platformID, targetID int64) (bool, error) {
	res, err := d.Exec(`DELETE FROM src_platform_targets WHERE platform_id=$1 AND target_id=$2`, platformID, targetID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SrcTargetConstraintText 把「平台规范 + 该平台下目标的授权范围」渲染成注入 Agent 的文本。
// 无内容返回 ""。两段都取,因为平台规范(奖励/评分/守则)与目标授权范围(能打哪些域名)
// 对模型是同等重要的边界信息。
func (d *DB) SrcTargetConstraintText(platformID, targetID int64) (string, error) {
	var b strings.Builder
	if platformID > 0 {
		text, err := d.SRCPlatformSpecText(platformID)
		if err != nil {
			return "", err
		}
		b.WriteString(text)
	}
	if platformID > 0 && targetID > 0 {
		link, err := d.GetSRCTargetLink(platformID, targetID)
		if err != nil {
			return "", err
		}
		if link != nil && link.Enabled {
			target, err := d.GetSRCTarget(targetID)
			if err != nil {
				return "", err
			}
			b.WriteString(renderSRCTargetScope(link, target))
		}
	}
	return b.String(), nil
}

// renderSRCTargetScope 渲染「平台×目标」这一层的授权范围为注入文本。抽成纯函数便于单测。
func renderSRCTargetScope(link *SRCTargetLink, target *SRCTarget) string {
	if link == nil {
		return ""
	}
	name := link.TargetName
	if name == "" && target != nil {
		name = target.Name
	}
	var b strings.Builder
	if strings.TrimSpace(link.ScopeNote) != "" {
		fmt.Fprintf(&b, "- 授权范围:%s\n", strings.TrimSpace(link.ScopeNote))
	}
	if strings.TrimSpace(link.AllowedTypes) != "" {
		fmt.Fprintf(&b, "- 允许的测试类型:%s\n", strings.TrimSpace(link.AllowedTypes))
	}
	if strings.TrimSpace(link.RateLimit) != "" {
		fmt.Fprintf(&b, "- 速率/强度限制(不得超出):%s\n", strings.TrimSpace(link.RateLimit))
	}
	// 禁止事项逐行 → 每行单独列出,便于规划者逐条遵守。
	if lines := splitNonEmptyLines(link.Forbidden); len(lines) > 0 {
		b.WriteString("- 严格禁止(违反即越界,任何理由都不例外):\n")
		for _, line := range lines {
			fmt.Fprintf(&b, "  · %s\n", line)
		}
	}
	if strings.TrimSpace(link.Rules) != "" {
		fmt.Fprintf(&b, "- 补充规范:\n%s\n", strings.TrimSpace(link.Rules))
	}
	if b.Len() == 0 {
		return ""
	}
	return fmt.Sprintf("【SRC 目标授权边界:%s】\n%s超出上述范围的任何动作一律不做;不确定是否越界时,选择不动作并说明。\n", name, b.String())
}

// splitNonEmptyLines 按行拆分并去掉每行前缀的列表符号与空白。
func splitNonEmptyLines(s string) []string {
	out := []string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// ---- 目标资产 ----

// SRCTargetAsset 是目标下的资产关联(联表带出资产展示字段)。
// 资产本体仍在 assets 表,这里只记「该平台该目标纳入了哪些资产」——
// 用引用而非复制,资产详情/覆盖度等既有链路完全不受影响。
type SRCTargetAsset struct {
	ID         int64     `json:"id"`
	PlatformID int64     `json:"platform_id"`
	TargetID   int64     `json:"target_id"`
	AssetID    int64     `json:"asset_id"`
	Source     string    `json:"source"`
	Note       string    `json:"note"`
	CreatedAt  time.Time `json:"created_at"`
	// 联表带出的资产展示字段(前端列表直接展示,不必再查一次资产接口)。
	Type      string `json:"type,omitempty"`
	Value     string `json:"value,omitempty"`
	Port      int    `json:"port,omitempty"`
	CompanyID *int64 `json:"company_id,omitempty"`
}

// srcTargetAssetValueExpr 按资产类型取「值」列:域名类取 domain,IP 取 ip,
// 服务/端点取 url,应用取 app_name。前端只关心一个可读的值,不该理解 assets 的列布局。
const srcTargetAssetValueExpr = `COALESCE(NULLIF(a.domain,''), NULLIF(a.url,''), NULLIF(a.ip,''), NULLIF(a.app_name,''), '')`

const srcTargetAssetCols = `ta.id, ta.platform_id, ta.target_id, ta.asset_id, ta.source, ta.note, ta.created_at,
	a.type, ` + srcTargetAssetValueExpr + `, COALESCE(a.port,0), a.company_id`

func scanSRCTargetAsset(sc interface{ Scan(...any) error }) (*SRCTargetAsset, error) {
	var x SRCTargetAsset
	if err := sc.Scan(&x.ID, &x.PlatformID, &x.TargetID, &x.AssetID, &x.Source, &x.Note, &x.CreatedAt,
		&x.Type, &x.Value, &x.Port, &x.CompanyID); err != nil {
		return nil, err
	}
	return &x, nil
}

// SRCTargetAssetFilter 是目标资产列表的筛选条件。
type SRCTargetAssetFilter struct {
	PlatformID int64
	Type       string // ""|"all" = 不过滤
	Q          string // 匹配资产值
	Limit      int
	Offset     int
}

// ListSRCTargetAssets 分页检索目标下的资产,并返回按 type 的计数。
// counts 不受 Type 过滤影响(分类器铁律:否则选中一类后其他类归零)。
func (d *DB) ListSRCTargetAssets(targetID int64, f SRCTargetAssetFilter) ([]*SRCTargetAsset, int, map[string]int, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	where := []string{"ta.target_id=$1"}
	args := []any{targetID}
	if f.PlatformID > 0 {
		where = append(where, fmt.Sprintf("ta.platform_id=$%d", len(args)+1))
		args = append(args, f.PlatformID)
	}
	base := strings.Join(where, " AND ")

	counts := map[string]int{}
	crows, err := d.Query(`SELECT a.type, COUNT(*) FROM src_target_assets ta
		JOIN assets a ON a.id=ta.asset_id WHERE `+base+` GROUP BY a.type`, args...)
	if err != nil {
		return nil, 0, nil, err
	}
	for crows.Next() {
		var typ string
		var n int
		if err := crows.Scan(&typ, &n); err != nil {
			crows.Close()
			return nil, 0, nil, err
		}
		counts[typ] = n
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return nil, 0, nil, err
	}

	filtered := base
	fargs := append([]any(nil), args...)
	if typ := strings.TrimSpace(f.Type); typ != "" && typ != "all" {
		filtered += fmt.Sprintf(" AND a.type=$%d", len(fargs)+1)
		fargs = append(fargs, typ)
	}
	if kw := strings.TrimSpace(f.Q); kw != "" {
		filtered += fmt.Sprintf(" AND "+srcTargetAssetValueExpr+` ILIKE $%d`, len(fargs)+1)
		fargs = append(fargs, "%"+kw+"%")
	}
	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM src_target_assets ta JOIN assets a ON a.id=ta.asset_id
		WHERE `+filtered, fargs...).Scan(&total); err != nil {
		return nil, 0, nil, err
	}

	query := `SELECT ` + srcTargetAssetCols + ` FROM src_target_assets ta
		JOIN assets a ON a.id=ta.asset_id WHERE ` + filtered +
		fmt.Sprintf(` ORDER BY ta.id DESC LIMIT $%d OFFSET $%d`, len(fargs)+1, len(fargs)+2)
	fargs = append(fargs, f.Limit, f.Offset)
	rows, err := d.Query(query, fargs...)
	if err != nil {
		return nil, 0, nil, err
	}
	defer rows.Close()
	out := []*SRCTargetAsset{}
	for rows.Next() {
		x, err := scanSRCTargetAsset(rows)
		if err != nil {
			return nil, 0, nil, err
		}
		out = append(out, x)
	}
	return out, total, counts, rows.Err()
}

// AddSRCTargetAssets 批量把已有资产挂到目标下(幂等:已存在的跳过)。
// 返回新增数与跳过数。assetIDs 为空时返回 (0,0,nil)。
func (d *DB) AddSRCTargetAssets(platformID, targetID int64, assetIDs []int64, source, note string) (int, int, error) {
	if platformID <= 0 || targetID <= 0 {
		return 0, 0, fmt.Errorf("平台与目标必填")
	}
	if source == "" {
		source = "manual"
	}
	switch source {
	case "manual", "recon", "existing":
	default:
		return 0, 0, fmt.Errorf("资产来源无效:%s", source)
	}
	added, skipped := 0, 0
	for _, assetID := range assetIDs {
		if assetID <= 0 {
			skipped++
			continue
		}
		// ON CONFLICT DO NOTHING 让重复导入天然幂等:信息搜集反复跑同一批资产不会报错。
		res, err := d.Exec(`
INSERT INTO src_target_assets(platform_id, target_id, asset_id, source, note)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (platform_id, target_id, asset_id) DO NOTHING`,
			platformID, targetID, assetID, source, note)
		if err != nil {
			return added, skipped, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added++
		} else {
			skipped++
		}
	}
	return added, skipped, nil
}

// RemoveSRCTargetAssets 按关联行 id 批量移除。返回删除行数。
func (d *DB) RemoveSRCTargetAssets(targetID int64, ids []int64) (int64, error) {
	if targetID <= 0 || len(ids) == 0 {
		return 0, nil
	}
	res, err := d.Exec(`DELETE FROM src_target_assets WHERE target_id=$1 AND id = ANY($2)`, targetID, ids)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// SrcTargetAssetIDs 返回目标下已关联的资产 id 集合(导入来源页据此置灰已导入项)。
func (d *DB) SrcTargetAssetIDs(targetID, platformID int64) ([]int64, error) {
	if targetID <= 0 {
		return []int64{}, nil
	}
	args := []any{targetID}
	where := "target_id=$1"
	if platformID > 0 {
		where += " AND platform_id=$2"
		args = append(args, platformID)
	}
	rows, err := d.Query(`SELECT asset_id FROM src_target_assets WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
