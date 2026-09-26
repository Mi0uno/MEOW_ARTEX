package server

// 监听器测活(契约 C2_CONTRACTS.md §24B,三期补丁 A v3.1;本文件为 Agent X 新增):
//
//	POST /api/c2/listeners/{id}/check
//	  → 200 {ok: bool, latency_ms: number|null, checks: [{item, ok, detail}]}
//
// 规则(§24B 逐条):
//   - 未运行监听器 → 单项「运行状态」false +「监听器未运行,请先启动」,不做后续探测;
//   - local 载体(http/ws/tcp):TCP dial bind 地址(优先运行时实际地址,端口 0
//     内核分配的场景才拿得到);http/ws 追加一次 GET /<url_path>/e——任何 HTTP 响应
//     = 通,包括 400/404/426(探测的是端口与服务活性,不是合法信封);
//   - vps 载体:SSH 转发健康(Manager.IngressRunning + ingress_error)+ dial
//     「VPS 机器公网 host:remote_port」端到端验证 SSH 转发链(转发链尽头就是
//     本地监听,端到端口通了本地半边必通,不再单拨 bind);失败 detail 区分
//     SSH 断开 / 端口拒绝 / 超时;
//   - oss:进程级探活(对象扫描 goroutine 存活)+ Manager.CheckOSS 签名
//     GetBucket(验证 endpoint/bucket/AK/SK 全链;未配置 → ok=false);
//   - kcp:进程级探活(UDP 无连接,端口拨号无意义),detail 注明「UDP 无连接,
//     进程级探活」;
//   - 全部检查限时 5s/项,永不阻塞;结果只返回不落库;
//     latency_ms = TCP dial 耗时(成功时,毫秒;oss 无 TCP dial,取签名探测耗时)。

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	c2center "github.com/Autumn-27/artex/c2center"
	pgdb "github.com/Autumn-27/artex/db"
)

// c2CheckItemTimeout 是单项检查的统一时限(§24B:全部检查限时 5s/项,永不阻塞)。
const c2CheckItemTimeout = 5 * time.Second

// c2ListenerCheckItem 是逐项检查结果(前端 ok 绿 / detail 红字)。
type c2ListenerCheckItem struct {
	Item   string `json:"item"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// c2ListenerCheckResult 是测活响应体(§24B 冻结形状)。
type c2ListenerCheckResult struct {
	OK        bool                  `json:"ok"`
	LatencyMS *int64                `json:"latency_ms"`
	Checks    []c2ListenerCheckItem `json:"checks"`
}

// checkC2Listener POST /api/c2/listeners/{id}/check。只读探测:结果只返回不落库,
// 任何单项失败都不阻塞响应(每项自带 5s 超时)。
func (s *Server) checkC2Listener(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	l, err := s.m.PG().GetC2Listener(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if l == nil {
		writeErr(w, 404, "监听器不存在")
		return
	}
	writeJSON(w, 200, s.c2CheckListener(l))
}

// c2CheckListener 逐项跑测活并汇总:总 ok = 全部项 ok。
func (s *Server) c2CheckListener(l *pgdb.C2Listener) c2ListenerCheckResult {
	res := c2ListenerCheckResult{Checks: []c2ListenerCheckItem{}}
	if l == nil {
		res.Checks = append(res.Checks, c2ListenerCheckItem{Item: "运行状态", OK: false, Detail: "监听器不存在"})
		return res
	}
	// 运行状态是一切探测的前提(§24B:运行中才有意义;未运行不做后续项)。
	if !s.c2ListenerRunning(l.ID) {
		res.Checks = append(res.Checks, c2ListenerCheckItem{Item: "运行状态", OK: false, Detail: "监听器未运行,请先启动"})
		return res
	}
	res.Checks = append(res.Checks, c2ListenerCheckItem{Item: "运行状态", OK: true, Detail: "运行时进程存活"})

	proto := l.Proto
	if proto == "" {
		proto = c2center.ProtoHTTP // 一期行回落 http(与 transportOf 同口径)
	}
	var items []c2ListenerCheckItem
	var latency *int64
	if l.HostKind == pgdb.C2ListenerHostKindVPS {
		// vps 载体:端到端口拨号已覆盖本地半边(转发链尽头就是它),不再单拨 bind。
		items, latency = s.c2CheckVPSItems(l)
	} else {
		switch proto {
		case c2center.ProtoKCP:
			items = s.c2CheckKCPItems(l)
		case c2center.ProtoOSS:
			items, latency = s.c2CheckOSSItems(l)
		default: // http/ws/tcp
			items, latency = s.c2CheckLocalItems(l)
		}
	}
	res.Checks = append(res.Checks, items...)
	res.LatencyMS = latency
	res.OK = true
	for _, it := range res.Checks {
		if !it.OK {
			res.OK = false
			break
		}
	}
	return res
}

// ---- local 载体(http/ws/tcp)----

// c2CheckLocalItems 本地端口拨号 + (http/ws)信道 GET 探测。
func (s *Server) c2CheckLocalItems(l *pgdb.C2Listener) ([]c2ListenerCheckItem, *int64) {
	proto := l.Proto
	if proto == "" {
		proto = c2center.ProtoHTTP
	}
	var latency *int64
	items := []c2ListenerCheckItem{}
	addr := s.c2ListenerDialAddr(l)
	ms, ok, detail := c2CheckDialItem(addr)
	if ok {
		latency = &ms
	}
	items = append(items, c2ListenerCheckItem{Item: "TCP 端口", OK: ok, Detail: detail})
	if !ok || (proto != c2center.ProtoHTTP && proto != c2center.ProtoWS) {
		return items, latency
	}
	// http/ws 追加信道探测(§24B:任何 HTTP 响应=通,包括 400/404/426 伪装)。
	path := c2ListenerChannelPath(l)
	code, perr := c2CheckHTTPProbe(addr, path, c2CheckItemTimeout)
	if perr != nil {
		items = append(items, c2ListenerCheckItem{Item: "信道响应", OK: false, Detail: "HTTP 探测失败: " + firstLine(perr.Error(), 200)})
		return items, latency
	}
	items = append(items, c2ListenerCheckItem{Item: "信道响应", OK: true,
		Detail: fmt.Sprintf("GET %s → HTTP %d(端口与服务活性正常)", path, code)})
	return items, latency
}

// c2ListenerDialAddr 测活拨号地址:优先运行时实际地址(端口 0 内核分配的场景),
// 回退静态 bind 地址;0.0.0.0/::/空 host 归一为 127.0.0.1(dial 语义)。
func (s *Server) c2ListenerDialAddr(l *pgdb.C2Listener) string {
	addr := ""
	if mgr := C2Manager(); mgr != nil {
		addr = mgr.ListenerAddr(l.ID)
	}
	if addr == "" {
		addr = c2ListenerDisplayAddr(l)
	}
	if host, port, err := net.SplitHostPort(addr); err == nil {
		switch host {
		case "", "0.0.0.0", "::":
			addr = net.JoinHostPort("127.0.0.1", port)
		}
	}
	return addr
}

// c2ListenerChannelPath 信道路径 /<url_path>/e(与 c2center.channelPath 同口径)。
func c2ListenerChannelPath(l *pgdb.C2Listener) string {
	base := strings.Trim(l.URLPath, "/")
	if base == "" {
		return "/e"
	}
	return "/" + base + "/e"
}

// c2CheckDialItem 一次 TCP dial:返回 (毫秒, ok, detail)。
func c2CheckDialItem(addr string) (int64, bool, string) {
	if addr == "" {
		return 0, false, "拨号地址不可得(bind 未配置且运行时地址未知)"
	}
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, c2CheckItemTimeout)
	elapsed := time.Since(start)
	if err != nil {
		return 0, false, c2ClassifyDialErr(err)
	}
	_ = conn.Close()
	ms := elapsed.Milliseconds()
	return ms, true, fmt.Sprintf("TCP %s 连通(%d ms)", addr, ms)
}

// c2ClassifyDialErr 拨号失败的 detail 分类(§24B:区分 拒绝/超时/其余)。
// Windows 的拒绝原文是「actively refused it」、类 Unix 是「connection refused」,
// 统一按 refused 关键词归类。
func c2ClassifyDialErr(err error) string {
	if err == nil {
		return ""
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return fmt.Sprintf("连接超时(%s)", c2CheckItemTimeout)
	}
	msg := err.Error()
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "refused"):
		return "连接被拒绝(端口未监听): " + firstLine(msg, 200)
	case strings.Contains(low, "no route to host"), strings.Contains(low, "unreachable"):
		return "网络不可达: " + firstLine(msg, 200)
	}
	return "连接失败: " + firstLine(msg, 200)
}

// c2CheckHTTPProbe 对 http/ws 信道发一次 GET(§24B):任何 HTTP 响应(包括
// 400/404/426 的伪装应答,ws 非 upgrade GET 的 404 同样算)都算通;只有传输层
// 失败(连不上/超时/非 HTTP 应答)返回错误。返回响应状态码。
func c2CheckHTTPProbe(addr, path string, timeout time.Duration) (int, error) {
	u := url.URL{Scheme: "http", Host: addr, Path: path}
	tr := &http.Transport{
		Proxy:             nil, // 测活直连,不走系统代理(NO_PROXY 未必覆盖所有部署)
		DialContext:       (&net.Dialer{Timeout: timeout}).DialContext,
		DisableKeepAlives: true,
	}
	client := &http.Client{
		Timeout: timeout, Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, nil
}

// ---- kcp 载体 ----

// c2CheckKCPItems kcp 载体(§24B):UDP 无连接,端口拨号无意义,做进程级探活
// (监听器 goroutine 存活 + UDP 本地端口绑定)。
func (s *Server) c2CheckKCPItems(l *pgdb.C2Listener) []c2ListenerCheckItem {
	detail := "UDP 无连接,进程级探活:监听器 goroutine 存活"
	if mgr := C2Manager(); mgr != nil {
		if addr := mgr.ListenerAddr(l.ID); addr != "" {
			detail += ",UDP 已绑定 " + addr
		}
	}
	return []c2ListenerCheckItem{{Item: "进程探活", OK: true, Detail: detail}}
}

// ---- oss 载体 ----

// c2CheckOSSItems oss 载体(§24B):无本地端口,进程级探活 + Manager.CheckOSS
// 签名 GetBucket(验证 endpoint/bucket/AK/SK 全链;未配置 → ok=false)。
// latency 取签名探测耗时(oss 无 TCP dial,这是最接近的等价量)。
func (s *Server) c2CheckOSSItems(l *pgdb.C2Listener) ([]c2ListenerCheckItem, *int64) {
	items := []c2ListenerCheckItem{{
		Item: "进程探活", OK: true,
		Detail: "oss 无监听端口,进程级探活:对象扫描 goroutine 存活",
	}}
	mgr := C2Manager()
	if mgr == nil {
		items = append(items, c2ListenerCheckItem{Item: "对象存储", OK: false, Detail: "C2 中心未初始化"})
		return items, nil
	}
	start := time.Now()
	err := mgr.CheckOSS(c2centerListenerFromDB(l))
	ms := time.Since(start).Milliseconds()
	if err != nil {
		items = append(items, c2ListenerCheckItem{Item: "对象存储", OK: false,
			Detail: "签名 GetBucket 失败: " + firstLine(err.Error(), 200)})
		return items, nil
	}
	latency := ms
	items = append(items, c2ListenerCheckItem{Item: "对象存储", OK: true,
		Detail: fmt.Sprintf("签名 GetBucket 通过(endpoint/bucket/AK/SK 全链正常,%d ms)", ms)})
	return items, &latency
}

// ---- vps 载体 ----

// c2CheckVPSItems vps 载体(§24B):SSH 转发健康 + dial 机器公网
// host:remote_port 端到端验证 SSH 转发链。失败 detail 区分 SSH 断开 /
// 转发端口拒绝 / 超时(c2ClassifyDialErr)。
func (s *Server) c2CheckVPSItems(l *pgdb.C2Listener) ([]c2ListenerCheckItem, *int64) {
	items := []c2ListenerCheckItem{}

	// SSH 健康:入口转发器在位(Manager 登记)且无最近异常标注。
	var sshOK bool
	var sshDetail string
	mgr := C2Manager()
	switch {
	case mgr == nil:
		sshDetail = "C2 中心未初始化"
	case !mgr.IngressRunning(l.ID):
		sshDetail = "SSH 入口转发不在位(已断开或未建立),请重启监听器重建转发"
		if e := getC2IngressErr(l.ID); e != "" {
			sshDetail += ": " + firstLine(e, 160)
		}
	default:
		sshOK = true
		sshDetail = "SSH 入口转发在位"
		if e := getC2IngressErr(l.ID); e != "" {
			sshOK = false
			sshDetail = "SSH 入口转发最近一次异常: " + firstLine(e, 160)
		}
	}
	items = append(items, c2ListenerCheckItem{Item: "SSH 转发", OK: sshOK, Detail: sshDetail})

	// 端到端:拨 VPS 机器公网 host:remote_port。
	var latency *int64
	vm, err := s.m.PG().GetVpsMachine(l.VPSMachineID)
	if err != nil {
		items = append(items, c2ListenerCheckItem{Item: "转发端口", OK: false, Detail: "查询关联 VPS 机器失败: " + firstLine(err.Error(), 200)})
		return items, nil
	}
	if vm == nil {
		items = append(items, c2ListenerCheckItem{Item: "转发端口", OK: false,
			Detail: fmt.Sprintf("关联的 VPS 机器 #%d 不存在", l.VPSMachineID)})
		return items, nil
	}
	host := strings.TrimSpace(vm.Host)
	if host == "" {
		items = append(items, c2ListenerCheckItem{Item: "转发端口", OK: false,
			Detail: fmt.Sprintf("关联的 VPS 机器 «%s» 未配置公网 host", vm.Name)})
		return items, nil
	}
	ms, ok, detail := c2CheckDialItem(net.JoinHostPort(host, strconv.Itoa(l.RemotePort)))
	if ok {
		latency = &ms
	}
	items = append(items, c2ListenerCheckItem{Item: "转发端口", OK: ok, Detail: detail})
	return items, latency
}
