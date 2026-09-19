//go:build windows

package desktop

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	webviewloader "github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

var current webview2.WebView

func available() bool {
	v, err := webviewloader.GetInstalledVersion()
	return err == nil && strings.TrimSpace(v) != ""
}

func run(url, title string, width, height int) error {
	// WebView2 的窗口与 COM 在当前 OS 线程上创建，Run 的消息循环也泵当前线程
	// 的队列（库内部没有 LockOSThread）。Go 调度器可能把 goroutine 迁移到别的
	// OS 线程（调用链里的 time.Sleep 就是触发点），一旦迁移，消息循环泵不到
	// 窗口的消息队列，窗口直接冻结。必须把窗口的完整生命周期钉在一条线程上。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  title,
			Width:  uint(width),
			Height: uint(height),
			Center: true,
			IconId: 1, // go-winres 生成的 APP 图标资源
		},
	})
	if w == nil {
		return errors.New("WebView2 运行时不可用")
	}
	current = w
	defer func() {
		current = nil
		w.Destroy()
	}()
	w.Navigate(url)
	w.Run()
	return nil
}

func terminate() {
	if w := current; w != nil {
		// Dispatch 把关闭动作投递到窗口所属的主线程，跨线程直接调
		// Terminate 会踩 COM 线程模型。
		w.Dispatch(func() { w.Terminate() })
	}
}

func hasConsole() bool {
	getConsoleWindow := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow")
	h, _, _ := getConsoleWindow.Call()
	return h != 0
}

func alert(title, msg string) {
	const (
		mbIconError = 0x00000010
		mbSetFG     = 0x00010000
		mbTopmost   = 0x00040000
	)
	t, errT := windows.UTF16PtrFromString(title)
	m, errM := windows.UTF16PtrFromString(msg)
	if errT != nil || errM != nil {
		return
	}
	MessageBoxW := windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW")
	_, _, _ = MessageBoxW.Call(
		0,
		uintptr(unsafe.Pointer(m)),
		uintptr(unsafe.Pointer(t)),
		mbIconError|mbSetFG|mbTopmost,
	)
}

// OpenExternal 在没有 WebView2 运行时的老机器上兜底：交给系统默认浏览器打开。
// 用 ShellExecuteW（协议处理器）而不是拼命令行，系统按 URL 协议自行分发。
func OpenExternal(url string) error {
	// 窗口/浏览器只允许指向本机服务；url 虽由主程序内部拼出，仍收紧到本地回环。
	if !isLocalURL(url) {
		return fmt.Errorf("desktop: 拒绝打开非本机地址 %q", url)
	}
	verb, errV := windows.UTF16PtrFromString("open")
	target, errU := windows.UTF16PtrFromString(url)
	if errV != nil || errU != nil {
		return fmt.Errorf("desktop: 非法地址 %q", url)
	}
	return windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
}

func isLocalURL(url string) bool {
	u := strings.ToLower(url)
	return strings.HasPrefix(u, "http://127.0.0.1:") ||
		strings.HasPrefix(u, "http://localhost:") ||
		strings.HasPrefix(u, "http://[::1]:")
}
