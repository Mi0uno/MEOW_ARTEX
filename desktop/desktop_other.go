//go:build !windows

package desktop

import "errors"

// 非 Windows 平台的桩：桌面窗口目前只在 Windows（WebView2）上提供，服务端
// 部署不受影响。接口与 windows 实现保持一一对应，编译期兜住跨平台构建。

func available() bool         { return false }
func hasConsole() bool        { return true }
func alert(title, msg string) {}
func terminate()              {}

func run(url, title string, width, height int) error {
	return errors.New("desktop: 桌面窗口仅支持 Windows（WebView2）")
}
