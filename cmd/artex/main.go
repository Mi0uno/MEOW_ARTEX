// Command artex runs the ARTEX backend: the dual SQLite graph stores,
// the event-driven exploration engine, and the JSON HTTP API consumed by the
// shadcn/ui frontend.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/config"
	"github.com/Autumn-27/artex/desktop"
	"github.com/Autumn-27/artex/selfupdate"
	"github.com/Autumn-27/artex/server"
)

// version is the build version, injected at release time via
// -ldflags "-X main.version=<tag>". Defaults to "dev" for local builds.
var version = "dev"

// desktopDefault marks a GUI (windowsgui subsystem) build: such a binary is
// meant to be double-clicked, so -desktop=auto opens the desktop window by
// default. Console builds keep serving headless. Injected via
// -ldflags "-X main.desktopDefault=1".
var desktopDefault = "0"

const banner = `
 __  __  _____   ___  __        __ _____  _   _   ____  _  __
|  \/  || ____| / _ \ \ \      / /|  ___|| | | | / ___|| |/ /
| |\/| ||  _|  | | | | \ \ /\ / / | |__  | | | || |    | ' /
| |  | || |___ | |_| |  \ V  V /  |  __| | |_| || |___ | . \
|_|  |_||_____| \___/    \_/\_/   |_|     \___/  \____||_|\_\
`

// printBanner writes the startup banner + version/runtime info to stdout.
func printBanner(addr string) {
	fmt.Print(banner)
	fmt.Println("  AI 自主渗透测试系统")
	fmt.Printf("  版本 %s  ·  %s/%s  ·  %s  ·  监听 %s\n\n",
		version, runtime.GOOS, runtime.GOARCH, runtime.Version(), addr)
}

// main only maps run's result onto the process exit code. The exit code is part
// of the update protocol — the supervising start script reads it to decide
// whether to relaunch us (see selfupdate.ExitRestart) — so the body has to live
// in a function that can *return* rather than os.Exit past its own defers.
func main() {
	os.Exit(run())
}

func run() int {
	var (
		addr         = flag.String("addr", ":8787", "HTTP listen address")
		dataDir      = flag.String("data", filepath.Join(config.BaseDir(), "data"), "data directory for SQLite stores (default: data/ next to the executable)")
		proxy        = flag.String("proxy", "127.0.0.1:8788", "traffic recording proxy address (empty to disable)")
		desktopParam = flag.String("desktop", "auto", "desktop window mode: on/off/auto (auto opens a WebView2 window for GUI builds)")
	)
	flag.Parse()

	// hand the build version to the server package so GET /api/health can report it
	// to the frontend top bar.
	server.BuildVersion = version

	useDesktop := desktop.Wanted(desktop.Mode(strings.TrimSpace(*desktopParam)), desktopDefault == "1")

	printBanner(*addr)

	// capture backend logs into the in-memory sink (still to stderr) so the /logs
	// page can show a live log stream. Do this first, to catch startup logs too.
	server.StartLogCapture()

	// Self-update bootstrap: swap in a staged binary, or count a post-swap boot
	// attempt and roll back if the new build keeps dying. Must run before we open
	// the stores or bind a port — this may end with "exit and let the start script
	// relaunch me", and there is no point paying for either first.
	action, upState := selfupdate.Bootstrap()
	server.SetBootUpdateState(upState)
	if action == selfupdate.Restart {
		return selfupdate.ExitRestart
	}

	baseURL := localURL(*addr)

	// 桌面模式下若已有实例在服务，直接开窗连过去，绝不二次起服务——第二个
	// 实例会抢 SQLite 存储和端口，只会互相打架。
	if useDesktop && desktop.Available() && healthy(baseURL) {
		log.Printf("[desktop] 检测到已运行的实例，直接连接 %s", baseURL)
		_ = desktop.Run(baseURL, "MEOWFUCK", 1440, 900)
		return 0
	}

	// surface which config file the binary reads (absolute, so `go run`'s relative
	// "config.json" — resolved against the CWD — is unambiguous).
	cfgPath := config.Path()
	if abs, e := filepath.Abs(cfgPath); e == nil {
		cfgPath = abs
	}
	if _, e := os.Stat(cfgPath); e == nil {
		log.Printf("[config] 配置文件: %s", cfgPath)
	} else {
		log.Printf("[config] 配置文件: %s (不存在 — 将仅尝试环境变量 ARTEX_PG_DSN)", cfgPath)
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, shutdown := shutdownContext(sigCtx)
	defer shutdown(agent.AbortShutdown)

	mgr, err := server.NewManager(*dataDir, *proxy)
	if err != nil {
		return fail(useDesktop, "ARTEX 启动失败", fmt.Sprintf("打开数据存储失败: %v", err))
	}
	defer mgr.Close()

	// Surviving this long means a freshly swapped-in build actually works, so drop
	// the upgrade marker and stop counting attempts. Until it fires, every boot
	// increments the count and a build that keeps dying gets rolled back.
	settle := time.AfterFunc(selfupdate.SettleDelay, selfupdate.Settle)
	defer settle.Stop()

	// 桌面模式下先绑定端口再开窗：绑定失败属于启动错误，应当弹窗而不是
	// 让窗口对着一个永远连不上的地址转圈。
	var ln net.Listener
	if useDesktop {
		ln, err = net.Listen("tcp", *addr)
		if err != nil {
			return fail(useDesktop, "ARTEX 启动失败", fmt.Sprintf("监听 %s 失败: %v（端口可能被占用，或已有实例在运行）", *addr, err))
		}
		defer ln.Close()
	}

	skillDir := config.SkillDir()
	if abs, err := filepath.Abs(skillDir); err == nil {
		skillDir = abs
	}
	log.Printf("[config] skill 目录: %s", skillDir)
	srv := server.New(ctx, mgr, skillDir, *dataDir, config.BaseDir())
	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// 慢速滴灌与空闲连接回收：未认证端点（/api/auth/*、/api/health）也能
		// 占满连接。不设 WriteTimeout——SSE 是长连接响应，设了会掐断直播流。
		ReadTimeout: 60 * time.Second,
		IdleTimeout: 120 * time.Second,
	}

	var serveFailed atomic.Bool
	go func() {
		log.Printf("MEOWFUCK %s backend listening on %s (data=%s, workers=%d)", version, *addr, *dataDir, mgr.Workers())
		var serveErr error
		if ln != nil {
			serveErr = httpSrv.Serve(ln)
		} else {
			serveErr = httpSrv.ListenAndServe()
		}
		if serveErr != nil && serveErr != http.ErrServerClosed {
			if useDesktop {
				// GUI 构建没有控制台，服务挂掉必须弹窗 + 收窗口，
				// 否则进程静默消失，用户只会觉得"双击没反应"。
				serveFailed.Store(true)
				fail(useDesktop, "ARTEX 服务异常", fmt.Sprintf("HTTP 服务异常退出: %v", serveErr))
				desktop.Terminate()
			} else {
				log.Fatalf("serve: %v", serveErr)
			}
		}
	}()

	code := 0
	if useDesktop {
		code = runDesktop(ctx, shutdown, baseURL, &serveFailed)
		if serveFailed.Load() && code == 0 {
			code = 1
		}
	} else {
		// Two ways out: a signal (normal stop → exit 0, the start script stops looping)
		// or a staged update / rollback (→ exit 75, the script relaunches us and the
		// bootstrap above installs the new build).
		select {
		case <-ctx.Done():
		case <-server.RestartRequested():
			code = selfupdate.ExitRestart
			shutdown(agent.AbortShutdown)
		}
	}

	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	return code
}

// runDesktop 打开桌面窗口并阻塞到窗口关闭，返回进程退出码。窗口被用户关闭
// 视为正常退出（走优雅停机）；升级/回滚请求仍以退出码 75 通知守护脚本。
func runDesktop(ctx context.Context, shutdown context.CancelCauseFunc, baseURL string, serveFailed *atomic.Bool) int {
	var restart atomic.Bool
	go func() {
		select {
		case <-ctx.Done():
		case <-server.RestartRequested():
			restart.Store(true)
			shutdown(agent.AbortShutdown)
		}
		desktop.Terminate()
	}()

	// 就绪等待与服务异常并发：服务 goroutine 已经失败（端口被抢/崩掉）时
	// 不再开窗，避免出现一个对着死服务转圈的窗口。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if healthy(baseURL) || serveFailed.Load() {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if serveFailed.Load() {
		return 1
	}

	if err := desktop.Run(baseURL, "MEOWFUCK", 1440, 900); err != nil {
		// 没有 WebView2 运行时的老机器：退回系统浏览器，退化为纯服务模式。
		log.Printf("[desktop] 桌面窗口不可用（%v），回退系统浏览器 %s", err, baseURL)
		if openErr := desktop.OpenExternal(baseURL); openErr != nil {
			log.Printf("[desktop] 打开系统浏览器失败: %v", openErr)
		}
		select {
		case <-ctx.Done():
		case <-server.RestartRequested():
			shutdown(agent.AbortShutdown)
			return selfupdate.ExitRestart
		}
		return 0
	}

	// 窗口已关（用户点 X 或升级请求触发 Terminate）。正常关闭在这里补一个
	// 优雅停机信号，让引擎/存储按正常路径收尾。
	if !restart.Load() {
		shutdown(agent.AbortShutdown)
		return 0
	}
	return selfupdate.ExitRestart
}

// fail 把启动期错误变成可见反馈：控制台模式打日志即退出；桌面模式（无控制台）
// 额外弹系统错误框，避免进程静默消失。返回值直接作为进程退出码。
func fail(gui bool, title, msg string) int {
	log.Print(msg)
	if gui {
		desktop.Alert(title, msg)
	}
	return 1
}

// localURL 把 -addr 的监听地址翻译成浏览器可达的本机 URL。全零/通配监听一律
// 落到 127.0.0.1，桌面窗口永远连本机。
func localURL(addr string) string {
	host := "127.0.0.1"
	if h, p, err := net.SplitHostPort(addr); err == nil {
		switch h {
		case "", "0.0.0.0", "::", "[::]":
			// 通配监听 → 本机回环
		default:
			host = h
		}
		return "http://" + host + ":" + p
	}
	// ":8787" 之外还给不出端口就别猜了，原样交回去，让后续健康检查去失败。
	return "http://" + host + addr
}

// healthy 探测 baseURL 是否已在服务 ARTEX。
func healthy(baseURL string) bool {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(baseURL + "/api/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var body struct {
		OK      bool   `json:"ok"`
		Service string `json:"service"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false
	}
	return body.OK && body.Service == "artex"
}

// shutdownContext deliberately does not derive from signalCtx. If it did, the
// parent's plain context.Canceled could win the race before AbortShutdown was
// attached to the child, losing the diagnostic cause in every running Agent.
func shutdownContext(signalCtx context.Context) (context.Context, context.CancelCauseFunc) {
	ctx, shutdown := context.WithCancelCause(context.Background())
	go func() {
		select {
		case <-signalCtx.Done():
			shutdown(agent.AbortShutdown)
		case <-ctx.Done():
		}
	}()
	return ctx, shutdown
}
