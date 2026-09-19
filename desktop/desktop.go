// Package desktop 把本机 Web UI 装进原生桌面窗口（Windows 上用 WebView2），
// 让 ARTEX 可以双击直达、不再经由浏览器标签页使用。
//
// 职责边界：本包只管"开窗口/关窗口/弹错误框"；HTTP 服务仍由 main 起在本地
// 端口上，窗口只是它的一个原生壳。窗口关闭即整个应用退出（与浏览器关标签
// 不同，这里没有其他入口了）。
package desktop

// Mode 是 -desktop 参数的取值：显式 on/off 直接生效；auto 交给构建形态决定。
type Mode string

const (
	ModeAuto Mode = "auto"
	ModeOn   Mode = "on"
	ModeOff  Mode = "off"
)

// Wanted 计算 -desktop 的最终取值。defaultDesktop 由 ldflags 注入（GUI 构建
// 置 1）：auto 模式下只有"构建时就声明是桌面程序"才默认开窗口；控制台构建
// （start.bat 守护、脚本化运行）永远保持纯服务模式，日志可见性不变。
func Wanted(mode Mode, defaultDesktop bool) bool {
	switch mode {
	case ModeOn:
		return true
	case ModeOff:
		return false
	default:
		return defaultDesktop
	}
}

// Available 报告当前平台能否打开桌面窗口。
func Available() bool { return available() }

// Run 打开原生窗口并加载 url，阻塞直到窗口被关闭（用户点 X 或 Terminate）。
// 调用方必须在 HTTP 服务就绪之后调用，且应在主 goroutine 上调用——WebView2
// 的消息循环对线程有要求。
func Run(url, title string, width, height int) error { return run(url, title, width, height) }

// Terminate 请求关闭 Run 打开的窗口（线程安全，可在任意 goroutine 调用）。
func Terminate() { terminate() }

// Alert 弹出一个系统错误框。GUI（windowsgui）构建没有控制台，启动失败时
// 必须有可见反馈，否则进程静默消失，用户只会觉得"双击没反应"。
func Alert(title, msg string) { alert(title, msg) }

// HasConsole 报告进程是否带着可见控制台。GUI 子系统构建返回 false。
func HasConsole() bool { return hasConsole() }
