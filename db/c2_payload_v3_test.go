package db

// 载荷矩阵 v3 列访问层单测(契约 C2_CONTRACTS.md §19,Agent P;测试连 5499):
// 建行带三期三列(CreateC2PayloadV3)、列表/详情带出(V3 读取器)、
// 矩阵列回填(SetC2PayloadMatrix)与构建产物回填(UpdateC2PayloadArtifact)、
// 白名单兜底校验。

import (
	"fmt"
	"testing"
	"time"
)

// TestC2PayloadV3Columns 三期矩阵列的建行/带出/回填全链路。
func TestC2PayloadV3Columns(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	l := c2TestListener(t, d, fmt.Sprintf("c2-payload-v3-%d", time.Now().UnixNano()))
	defer d.DeleteC2Listener(l.ID) //nolint:errcheck

	token := fmt.Sprintf("tokv3-%d", time.Now().UnixNano())
	pid, err := d.CreateC2PayloadV3(C2PayloadInput{
		ListenerID: l.ID, Name: "shellcode-staged", Format: C2PayloadFormatGoSrc, Target: "windows",
		Token: token, SHA256: "srcsha", Size: 2048, CreatedBy: "test",
	}, C2PayloadArtifactShellcode, C2DirectionBind, true)
	if err != nil {
		t.Fatalf("建 v3 载荷失败: %v", err)
	}
	defer d.DeleteC2Payload(pid) //nolint:errcheck

	// 详情带出(V3 读取器,一/二期字段完整)。
	pv, err := d.GetC2PayloadV3(pid)
	if err != nil || pv == nil {
		t.Fatalf("回读 v3 载荷失败: %v(%v)", pv, err)
	}
	if pv.Artifact != C2PayloadArtifactShellcode || pv.Direction != C2DirectionBind || !pv.Staged {
		t.Fatalf("v3 列不对: %+v", pv.C2Payload)
	}
	if pv.Format != C2PayloadFormatGoSrc || pv.Token != token || pv.EvasionProfileID != 0 {
		t.Fatalf("一期字段不对: %+v", pv.C2Payload)
	}
	if pv.BuiltOnVPS {
		t.Fatalf("构建回填前 built_on_vps 应为 false")
	}

	// token 读取器(stager 的 stage_get 平台侧解析走这条)。
	byToken, err := d.GetC2PayloadV3ByToken(token)
	if err != nil || byToken == nil || byToken.ID != pid {
		t.Fatalf("按 token 读 v3 载荷失败: %v(%v)", byToken, err)
	}

	// 列表带出。
	list, err := d.ListC2PayloadsV3(l.ID)
	if err != nil {
		t.Fatalf("列 v3 载荷失败: %v", err)
	}
	found := false
	for _, p := range list {
		if p.ID == pid {
			found = true
			if p.Artifact != C2PayloadArtifactShellcode || !p.Staged {
				t.Fatalf("列表行 v3 列不对: %+v", p.C2Payload)
			}
		}
	}
	if !found {
		t.Fatalf("列表缺载荷 #%d", pid)
	}

	// 矩阵列回填(SetC2PayloadMatrix)。
	if err := d.SetC2PayloadMatrix(pid, C2PayloadArtifactExe, C2DirectionReverse, false); err != nil {
		t.Fatalf("回填矩阵列失败: %v", err)
	}
	if got, _ := d.GetC2PayloadV3(pid); got == nil || got.Artifact != C2PayloadArtifactExe ||
		got.Direction != C2DirectionReverse || got.Staged {
		t.Fatalf("矩阵列回填未生效: %+v", got)
	}

	// 构建产物回填(UpdateC2PayloadArtifact):file/sha/size 换产物,built_on_vps=true。
	if err := d.UpdateC2PayloadArtifact(pid, "/data/c2/payloads/9_shellcode.bin", "binsha", 4096000, C2PayloadArtifactShellcode); err != nil {
		t.Fatalf("回填构建产物失败: %v", err)
	}
	got, err := d.GetC2PayloadV3(pid)
	if err != nil || got == nil {
		t.Fatalf("回读失败: %v(%v)", got, err)
	}
	if !got.BuiltOnVPS || got.SHA256 != "binsha" || got.Size != 4096000 ||
		got.Artifact != C2PayloadArtifactShellcode {
		t.Fatalf("构建产物回填未生效: %+v", got.C2Payload)
	}

	// 白名单兜底。
	if err := d.SetC2PayloadMatrix(pid, "msi", C2DirectionReverse, false); err == nil {
		t.Fatalf("非法 artifact 应被拒绝")
	}
	if err := d.SetC2PayloadMatrix(pid, C2PayloadArtifactExe, "both", false); err == nil {
		t.Fatalf("非法 direction 应被拒绝")
	}
	if err := d.UpdateC2PayloadArtifact(pid, "/tmp/x", "sha", 1, "msi"); err == nil {
		t.Fatalf("UpdateC2PayloadArtifact 非法 artifact 应被拒绝")
	}
	if _, err := d.CreateC2PayloadV3(C2PayloadInput{
		ListenerID: l.ID, Format: C2PayloadFormatGoSrc, Target: "windows", Token: token + "-x",
	}, "nope", C2DirectionReverse, false); err == nil {
		t.Fatalf("CreateC2PayloadV3 非法 artifact 应被拒绝")
	}
}

// TestC2PayloadV3Defaults 空值默认与一期路径兼容:CreateC2Payload(一期)建的行,
// V3 读取器应带出 schema 默认 script/reverse/false。
func TestC2PayloadV3Defaults(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	l := c2TestListener(t, d, fmt.Sprintf("c2-payload-v3def-%d", time.Now().UnixNano()))
	defer d.DeleteC2Listener(l.ID) //nolint:errcheck

	pid, err := d.CreateC2Payload(C2PayloadInput{
		ListenerID: l.ID, Name: "一期 ps1", Format: C2PayloadFormatPs1, Target: "windows",
		Token: fmt.Sprintf("tokv3def-%d", time.Now().UnixNano()), CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("建一期载荷失败: %v", err)
	}
	defer d.DeleteC2Payload(pid) //nolint:errcheck

	pv, err := d.GetC2PayloadV3(pid)
	if err != nil || pv == nil {
		t.Fatalf("回读失败: %v(%v)", pv, err)
	}
	if pv.Artifact != C2PayloadArtifactScript || pv.Direction != C2DirectionReverse || pv.Staged {
		t.Fatalf("一期行的 v3 默认列不对: %+v", pv.C2Payload)
	}
}
