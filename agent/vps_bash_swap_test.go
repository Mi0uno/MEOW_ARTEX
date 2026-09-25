package agent

import (
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

// 三角色(worker/planner/main)的 Bash 替换必须同走 applyVPSBashSwap:这是
// 「指派机器后命令绝不落本机(掉马甲)」修复的回归锚点——谁绕开这个入口单拼
// 工具集,这里过不去就说明又有一条本机执行通道出现了。
func TestApplyVPSBashSwap(t *testing.T) {
	t.Run("nil context 或 nil Bash 原样返回", func(t *testing.T) {
		local := actool.NewBash()
		tools := []actool.CoreTool{local}
		if got := applyVPSBashSwap(tools, nil); len(got) != 1 || got[0] != local {
			t.Fatalf("nil context 应原样返回")
		}
		if got := applyVPSBashSwap(tools, &VPSRunContext{}); len(got) != 1 || got[0] != local {
			t.Fatalf("Bash 为 nil 的 context 应原样返回")
		}
	})

	t.Run("同名 Bash 被替换为远程实现", func(t *testing.T) {
		remote := actool.NewBash()
		swapped := applyVPSBashSwap([]actool.CoreTool{actool.NewBash()}, &VPSRunContext{Bash: remote})
		if len(swapped) != 1 || swapped[0] != remote {
			t.Fatalf("同名 Bash 应被整体替换为远程实现")
		}
	})

	t.Run("无同名工具时不发生替换", func(t *testing.T) {
		tools := defaultToolsExcept("Bash") // 全是非 Bash 工具
		orig := append([]actool.CoreTool{}, tools...)
		swapped := applyVPSBashSwap(tools, &VPSRunContext{Bash: actool.NewBash()})
		if len(swapped) != len(orig) {
			t.Fatalf("工具数量不应变化: got %d want %d", len(swapped), len(orig))
		}
		for i := range swapped {
			if swapped[i] != orig[i] {
				t.Fatalf("位置 %d 的非同名工具不应被替换", i)
			}
		}
	})

	t.Run("多工具列表中原位替换保持顺序", func(t *testing.T) {
		tools := defaultToolsExcept("Bash")
		tools = append(tools, actool.NewBash())
		remote := actool.NewBash()
		swapped := applyVPSBashSwap(tools, &VPSRunContext{Bash: remote})
		if len(swapped) == 0 || swapped[len(swapped)-1] != remote {
			t.Fatalf("末尾的 Bash 应被原位替换且顺序不变")
		}
	})
}
