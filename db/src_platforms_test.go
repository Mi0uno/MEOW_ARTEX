package db

// SRC 平台选择(平台 → 目标 → 资产)的 PG 测试。
//
// 重点覆盖三处「错了就会静默丢数据/丢约束」的地方:
//  1. 唯一键与 nkey 归一化——重名判定必须与 schema.sql 的 src_nkey() 一致,
//     否则迁移建的行与运行时建的行会重复,平台列表出现同名两条。
//  2. 授权范围挂在「平台×目标」而不是目标上——同一目标在两个平台下必须是两份独立数据。
//  3. 约束文本渲染——禁止事项必须逐行拆开注入,整段塞入会被模型概括后丢细节。

import (
	"strings"
	"testing"
)

// srcTestFixtures 建一对平台与一个目标,返回 id 与清理函数。
//
// 清理必须由调用方 `defer` 执行(不能用 t.Cleanup):t.Cleanup 在测试函数返回后
// 才跑,那时 `defer d.Close()` 已经关掉连接,删除语句会静默失败、留下残留行——
// 下一次运行就会撞上「同名平台已存在」。既有测试(companies_test.go 等)同样用 defer。
func srcTestFixtures(t *testing.T, d *DB) (platformID, otherPlatformID, targetID int64, cleanup func()) {
	t.Helper()
	suffix := strings.ToLower(t.Name())
	p, err := d.CreateSRCPlatform(SRCPlatformInput{Name: "TestPlatform-" + suffix, Enabled: true})
	if err != nil {
		t.Fatalf("create platform: %v", err)
	}
	p2, err := d.CreateSRCPlatform(SRCPlatformInput{Name: "TestPlatform2-" + suffix, Enabled: true})
	if err != nil {
		d.Exec(`DELETE FROM src_platforms WHERE id=$1`, p.ID)
		t.Fatalf("create second platform: %v", err)
	}
	tg, err := d.CreateSRCTarget(SRCTargetInput{Name: "TestTarget-" + suffix, Enabled: true})
	if err != nil {
		d.Exec(`DELETE FROM src_platforms WHERE id = ANY($1)`, []int64{p.ID, p2.ID})
		t.Fatalf("create target: %v", err)
	}
	// 删平台会级联清掉挂链与资产关联;目标单独删。
	cleanup = func() {
		d.Exec(`DELETE FROM src_platforms WHERE id = ANY($1)`, []int64{p.ID, p2.ID})
		d.Exec(`DELETE FROM src_targets WHERE id=$1`, tg.ID)
	}
	return p.ID, p2.ID, tg.ID, cleanup
}

// TestSRCPlatformNameUniqueAndNKey 重名(含大小写/多余空格差异)必须被拒。
// 这是 nkey 归一化的核心价值:用户在两个页面分别输入「补天」与「补天 」不该建出两条。
func TestSRCPlatformNameUniqueAndNKey(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	name := "NKey Platform " + strings.ToLower(t.Name())
	p, err := d.CreateSRCPlatform(SRCPlatformInput{Name: name, Enabled: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { d.Exec(`DELETE FROM src_platforms WHERE id=$1`, p.ID) }()

	// 归一化 = lower + 折叠空白。所以大小写、前后空白、内部连续空白(含制表符/全角空格)
	// 都应落到同一个键;这里逐种变体确认都被判为重名。
	for _, variant := range []string{
		strings.ToUpper(name),
		"  " + name + "  ",
		strings.ReplaceAll(name, " ", "   "),     // 多个半角空格折叠成一个
		strings.ReplaceAll(name, " ", "\t"),      // 制表符同样折叠成半角空格
		strings.ReplaceAll(name, " ", "\u3000"),  // 全角空格(中文输入法常见)
	} {
		if _, err := d.CreateSRCPlatform(SRCPlatformInput{Name: variant, Enabled: true}); err != ErrSRCNameTaken {
			t.Fatalf("CreateSRCPlatform(%q) err=%v, want ErrSRCNameTaken", variant, err)
		}
	}
	// Go 侧与 SQL 侧归一化必须一致,否则迁移建的行与运行时建的行会重复。
	for _, probe := range []string{
		strings.ToUpper(name),
		"  补天  漏洞盒子  ",
		"补天\u3000漏洞盒子", // 全角空格
		"补天\t漏洞盒子",
		"补天\u00a0漏洞盒子", // 不换行空格
	} {
		var sqlNKey string
		if err := d.QueryRow(`SELECT src_nkey($1)`, probe).Scan(&sqlNKey); err != nil {
			t.Fatal(err)
		}
		if got := SrcNKey(probe); got != sqlNKey {
			t.Fatalf("SrcNKey(%q)=%q, SQL src_nkey=%q — 两侧归一化必须一致", probe, got, sqlNKey)
		}
	}
}

// TestSRCTargetLinkIsPerPlatform 同一目标在两个平台下的授权范围互相独立。
// 这是多对多模型的核心语义:把授权范围挂在目标上就无法表达「补天允许爆破、
// 教育SRC 禁止爆破」这种差异。
func TestSRCTargetLinkIsPerPlatform(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	p1, p2, target, cleanup := srcTestFixtures(t, d)
	defer cleanup()

	if _, err := d.LinkSRCTarget(p1, target, SRCTargetLinkInput{
		ScopeNote: "仅限 *.a.example.com", Forbidden: "禁止 DoS\n禁止爆破",
		RateLimit: "≤ 10 请求/秒", Enabled: true,
	}); err != nil {
		t.Fatalf("link p1: %v", err)
	}
	if _, err := d.LinkSRCTarget(p2, target, SRCTargetLinkInput{
		ScopeNote: "仅限 *.b.example.com", AllowedTypes: "仅 Web 漏洞", Enabled: true,
	}); err != nil {
		t.Fatalf("link p2: %v", err)
	}

	l1, err := d.GetSRCTargetLink(p1, target)
	if err != nil || l1 == nil {
		t.Fatalf("get link p1: %v %v", l1, err)
	}
	l2, err := d.GetSRCTargetLink(p2, target)
	if err != nil || l2 == nil {
		t.Fatalf("get link p2: %v %v", l2, err)
	}
	if l1.ScopeNote != "仅限 *.a.example.com" || l2.ScopeNote != "仅限 *.b.example.com" {
		t.Fatalf("scope 串台: p1=%q p2=%q", l1.ScopeNote, l2.ScopeNote)
	}
	if l1.Forbidden == l2.Forbidden {
		t.Fatalf("两平台的 forbidden 不应相同(各自独立): p1=%q", l1.Forbidden)
	}

	// 一个目标可挂多个平台;解绑一个平台不能影响另一个。
	links, err := d.ListSRCTargetLinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 {
		t.Fatalf("links=%d, want 2", len(links))
	}
	if ok, err := d.UnlinkSRCTarget(p1, target); err != nil || !ok {
		t.Fatalf("unlink p1: ok=%v err=%v", ok, err)
	}
	if l, err := d.GetSRCTargetLink(p1, target); err != nil || l != nil {
		t.Fatalf("p1 关联应已解除: %v %v", l, err)
	}
	if l, err := d.GetSRCTargetLink(p2, target); err != nil || l == nil {
		t.Fatalf("解绑 p1 不该影响 p2: %v %v", l, err)
	}
	// 目标本身必须还在(解绑只解除关联)。
	if tg, err := d.GetSRCTarget(target); err != nil || tg == nil {
		t.Fatalf("目标不该被解绑删除: %v %v", tg, err)
	}
}

// TestSRCTargetConstraintTextRendersPerLine 约束文本:禁止事项逐行拆分。
func TestSRCTargetConstraintTextRendersPerLine(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	p1, _, target, cleanup := srcTestFixtures(t, d)
	defer cleanup()
	if _, err := d.UpdateSRCPlatform(p1, SRCPlatformInput{
		Name: "SpecPlatform-" + strings.ToLower(t.Name()),
		// 平台级规范:四类文本域 + 附件正文都会参与注入。
		RewardStandard: "严重 3000 元", CodeOfConduct: "保持最小影响面", Enabled: true,
	}); err != nil {
		t.Fatalf("update platform: %v", err)
	}
	if _, err := d.LinkSRCTarget(p1, target, SRCTargetLinkInput{
		ScopeNote: "仅限 example.com", Forbidden: "禁止 DoS\n- 禁止上传 webshell\n\n禁止社工",
		RateLimit: "≤ 5 请求/秒", Enabled: true,
	}); err != nil {
		t.Fatalf("link: %v", err)
	}
	// 附件正文必须参与注入,否则用户上传的奖励标准对 AI 就是死文件。
	if _, err := d.CreateSRCPlatformFile(SRCPlatformFile{
		PlatformID: p1, Kind: "reward_standard", Filename: "奖励标准.docx",
		StoredName: "f-test.docx", ByteSize: 10, ContentText: "第一章 奖励范围",
		ParseStatus: "parsed",
	}); err != nil {
		t.Fatalf("create file: %v", err)
	}

	text, err := d.SrcTargetConstraintText(p1, target)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"严重 3000 元",              // 平台奖励标准
		"保持最小影响面",              // 平台守则
		"第一章 奖励范围",             // 附件解析正文
		"仅限 example.com",         // 目标授权范围
		"禁止 DoS",                 // 逐行拆出的禁止事项
		"禁止上传 webshell",          // 行首 "-" 前缀应被剥掉
		"禁止社工",                  // 空行被跳过但内容保留
		"≤ 5 请求/秒",               // 速率限制
		"超出上述范围的任何动作一律不做", // 兜底句
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("约束文本缺少 %q\n--- 实际 ---\n%s", want, text)
		}
	}
	// 停用关联后不该再注入该目标的授权范围(但平台规范仍在)。
	if _, err := d.LinkSRCTarget(p1, target, SRCTargetLinkInput{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	text2, err := d.SrcTargetConstraintText(p1, target)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text2, "仅限 example.com") {
		t.Fatalf("停用的关联不该注入授权范围:\n%s", text2)
	}
	if !strings.Contains(text2, "严重 3000 元") {
		t.Fatalf("平台规范应仍然注入:\n%s", text2)
	}
}

// TestSRCTargetAssetsIdempotent 资产挂链必须幂等:信息搜集反复导入同一批不该报错、
// 也不该出现重复行(否则资产数会被重复计数)。
func TestSRCTargetAssetsIdempotent(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	p1, _, target, cleanup := srcTestFixtures(t, d)
	defer cleanup()

	as := d.Assets()
	a1, err := as.UpsertRootDomain(UpsertRootDomainReq{Domain: "srctest-" + strings.ToLower(t.Name()) + ".example.com"})
	if err != nil {
		t.Fatalf("upsert root domain: %v", err)
	}
	a2, err := as.UpsertIP(UpsertIPReq{IP: "203.0.113.201"})
	if err != nil {
		t.Fatalf("upsert ip: %v", err)
	}
	defer func() {
		d.Exec(`DELETE FROM src_target_assets WHERE asset_id = ANY($1)`, []int64{a1, a2})
		d.Exec(`DELETE FROM assets WHERE id = ANY($1)`, []int64{a1, a2})
	}()

	added, skipped, err := d.AddSRCTargetAssets(p1, target, []int64{a1, a2}, "recon", "首次导入")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if added != 2 || skipped != 0 {
		t.Fatalf("首次导入 added=%d skipped=%d, want 2/0", added, skipped)
	}
	// 重复导入:全部跳过,不报错。
	added2, skipped2, err := d.AddSRCTargetAssets(p1, target, []int64{a1, a2}, "recon", "重复导入")
	if err != nil {
		t.Fatalf("重复导入不该报错: %v", err)
	}
	if added2 != 0 || skipped2 != 2 {
		t.Fatalf("重复导入 added=%d skipped=%d, want 0/2", added2, skipped2)
	}
	// 非法来源必须被拒(避免写进 CHECK 之外的值)。
	if _, _, err := d.AddSRCTargetAssets(p1, target, []int64{a1}, "bogus", ""); err == nil {
		t.Fatal("非法 source 应被拒绝")
	}

	items, total, counts, err := d.ListSRCTargetAssets(target, SRCTargetAssetFilter{PlatformID: p1, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("total=%d len=%d, want 2/2(重复导入不该产生重复行)", total, len(items))
	}
	if counts["root_domain"] != 1 || counts["ip"] != 1 {
		t.Fatalf("counts=%v, want root_domain=1 ip=1", counts)
	}

	// 类型过滤:counts 必须仍是未过滤全量(分类器铁律)。
	_, totalIP, countsIP, err := d.ListSRCTargetAssets(target, SRCTargetAssetFilter{PlatformID: p1, Type: "ip", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if totalIP != 1 {
		t.Fatalf("type=ip total=%d, want 1", totalIP)
	}
	if countsIP["root_domain"] != 1 {
		t.Fatalf("counts 不该被 Type 过滤影响: %v", countsIP)
	}

	// 移除关联:资产本体必须保留(只解关联,不删资产)。
	ids := []int64{items[0].ID}
	if n, err := d.RemoveSRCTargetAssets(target, ids); err != nil || n != 1 {
		t.Fatalf("remove: n=%d err=%v", n, err)
	}
	var stillThere int
	if err := d.QueryRow(`SELECT count(*) FROM assets WHERE id = $1`, items[0].AssetID).Scan(&stillThere); err != nil {
		t.Fatal(err)
	}
	if stillThere != 1 {
		t.Fatal("移除关联后资产本体必须仍存在")
	}
}

// TestSRCPlatformListCountsUnaffectedByFilter 分类器计数铁律:counts 不受 enabled 过滤影响。
func TestSRCPlatformListCountsUnaffectedByFilter(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	on, off, _, cleanup := srcTestFixtures(t, d)
	defer cleanup()
	// 两个平台共用同一前缀,保证关键字能同时命中启用与停用两条。
	prefix := "CountsPlatform-" + strings.ToLower(t.Name())
	if _, err := d.UpdateSRCPlatform(on, SRCPlatformInput{Name: prefix + "-on", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.UpdateSRCPlatform(off, SRCPlatformInput{Name: prefix + "-off", Enabled: false}); err != nil {
		t.Fatal(err)
	}

	_, _, counts, err := d.ListSRCPlatforms(prefix, "enabled", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if counts["enabled"] != 1 || counts["disabled"] != 1 {
		t.Fatalf("counts=%v, want enabled=1 disabled=1(计数必须来自未过滤全量)", counts)
	}
	items, total, _, err := d.ListSRCPlatforms(prefix, "enabled", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != on {
		t.Fatalf("过滤后 total=%d items=%d, want 1/1 且为启用项", total, len(items))
	}
}
