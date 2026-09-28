package db

// 漏洞库 finding 唯一约束的集成测试(并发双入库竞态的 DB 侧封线):
// idx_vulndb_finding_unique 部分唯一索引由 schema.sql 启动时建,前置的「去重三连」
// (PoC 并入 → poc_count 重算 → 删重复行)先清历史数据再建索引。本测试验证:
// ① 全库不再有同 finding 重复条目(清理已生效);
// ② 同一 finding 第二次 CreateVulnEntry 必须被唯一索引拒绝;
// ③ FindVulnEntryByFinding 仍指向首条(应用层兜底关联路径可用)。

import (
	"strings"
	"testing"
	"time"
)

func TestVulndbFindingUnique(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	var dupGroups int
	if err := d.QueryRow(`SELECT count(*) FROM (
		SELECT finding_id FROM vulndb_entries WHERE finding_id IS NOT NULL
		GROUP BY finding_id HAVING count(*) > 1) t`).Scan(&dupGroups); err != nil {
		t.Fatalf("查重失败: %v", err)
	}
	if dupGroups != 0 {
		t.Fatalf("库中仍有 %d 组同 finding 重复条目(schema 清理三连未生效?)", dupGroups)
	}

	// 超大假 finding_id:远超真实 findings 序列范围,无外键,绝不相交。
	// (FindVulnEntryByFinding 对 <=0 直接返回 nil,不能用负数。)
	fid := 9_000_000_000_000_000 + time.Now().UnixNano()%1_000_000
	title := "VUQ-finding-unique 测试条目"
	e1, err := d.CreateVulnEntry(VulnEntryInput{
		Category: VulnCategoryNDay, Title: title, Source: "ai_finding", FindingID: &fid,
	})
	if err != nil {
		t.Fatalf("首次入库失败: %v", err)
	}
	defer d.DeleteVulnEntry(e1.ID) //nolint:errcheck

	if _, err := d.CreateVulnEntry(VulnEntryInput{
		Category: VulnCategoryNDay, Title: title + " 二次", Source: "ai_finding", FindingID: &fid,
	}); err == nil {
		t.Fatal("同 finding 第二次入库未被 idx_vulndb_finding_unique 拒绝")
	} else if !strings.Contains(err.Error(), "duplicate key") && !strings.Contains(err.Error(), "idx_vulndb_finding_unique") {
		t.Fatalf("拒绝原因不是唯一索引冲突: %v", err)
	}

	ex, err := d.FindVulnEntryByFinding(fid)
	if err != nil || ex == nil {
		t.Fatalf("FindVulnEntryByFinding 应指向首条: got %v, err %v", ex, err)
	}
	if ex.ID != e1.ID {
		t.Fatalf("指向条目不符: got #%d want #%d", ex.ID, e1.ID)
	}
}
