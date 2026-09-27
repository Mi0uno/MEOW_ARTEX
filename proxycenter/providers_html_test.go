package proxycenter

// 表格感知 HTML 提取器与源请求头的单测:全部离线,fixture 取自三类真实站点的
// 简化形态(快代理式多列表格 / proxy5 式带协议列 / 站大爷式 li 行)。
// 这些测试是「表格站从 0 条变有数据」的最低保证,也是端口判定宁缺毋滥的回归网。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fixtureKuaidaili 式:IP/端口分列的多列表格,行里混日期、延迟等数字干扰列。
// 「1.2.3.4:9999」一行是整格 host:port 形态;「9.9.9.9」一行端口列被两个
// 同分候选(8380/9999)占住,按宁缺毋滥应整行放弃。
const fixtureKuaidaili = `<!doctype html><html><body><table>
<thead><tr>
  <th>IP 地址</th><th>端口</th><th>匿名度</th><th>位置</th><th>响应速度</th><th>最后验证</th>
</tr></thead>
<tbody>
<tr><td>1.10.141.115</td><td>8080</td><td>高匿名</td><td>吉林省延边州 联通</td><td>0.38秒</td><td>09-27 15:00:01</td></tr>
<tr><td>117.91.226.111</td><td>3256</td><td>高匿名</td><td>江苏镇江 电信</td><td>0.53秒</td><td>09-27 14:59:44</td></tr>
<tr><td>36.26.82.135</td><td>8256</td><td>透明</td><td>广西南宁 电信</td><td>1.2秒</td><td>09-27 14:58:10</td></tr>
<tr><td>1.10.141.115</td><td>8080</td><td>高匿名</td><td>吉林省延边州 联通</td><td>0.38秒</td><td>09-27 15:00:01</td></tr>
<tr><td>127.0.0.1</td><td>8080</td><td>高匿名</td><td>本地</td><td>0.01秒</td><td>09-27 15:00:02</td></tr>
<tr><td>192.168.1.3</td><td>9999</td><td>透明</td><td>内网</td><td>0.02秒</td><td>09-27 15:00:03</td></tr>
<tr><td>9.9.9.9</td><td>8380</td><td>高匿名</td><td>测试</td><td>0.11秒</td><td>9999</td></tr>
<tr><td>8.8.4.4:9999</td><td>高匿名</td><td>测试</td></tr>
</tbody></table></body></html>`

// TestHTMLTableExtractKuaidailiStyle 多列表格:只产出配对成功的行,干扰列不误报。
func TestHTMLTableExtractKuaidailiStyle(t *testing.T) {
	got := htmlTableExtract([]byte(fixtureKuaidaili), "")
	want := []string{"1.10.141.115:8080", "117.91.226.111:3256", "36.26.82.135:8256", "8.8.4.4:9999"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("提取结果不符:\n got  %v\n want %v", got, want)
	}
}

// fixtureProxy5 式:带协议列与状态列的英文表格,端口列是纯数字、其余列含
// "18 min"/"91% (478 / 481)" 一类数字噪声。
const fixtureProxy5 = `<!doctype html><html><body><table id="proxy-table">
<tr><th>代理 IP</th><th>代理端口</th><th>代理类型</th><th>匿名</th><th>稳定性</th><th>最后检查</th></tr>
<tr><td class="font-monospace">103.179.252.170</td><td>3127</td><td>HTTP</td><td>Transparent</td><td>91% (478 / 481)</td><td>18 min</td></tr>
<tr><td class="font-monospace">45.12.33.87</td><td>8080</td><td>HTTPS</td><td>Anonymous</td><td>99% (99 / 100)</td><td>2 min</td></tr>
<tr><td class="font-monospace">103.153.62.155</td><td>1080</td><td>SOCKS5</td><td>Elite</td><td>77% (7 / 9)</td><td>7 min</td></tr>
</table></body></html>`

// TestHTMLTableExtractProtocolColumn 表格里的协议列/百分比列不参与端口判定。
func TestHTMLTableExtractProtocolColumn(t *testing.T) {
	got := htmlTableExtract([]byte(fixtureProxy5), "")
	want := []string{"103.179.252.170:3127", "45.12.33.87:8080", "103.153.62.155:1080"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("提取结果不符:\n got  %v\n want %v", got, want)
	}
}

// fixtureZdaye 式:站大爷的真实形态不是 table,是 li 里两个 <p>(IP 一段、
// "Port：30001" 一段,全角冒号),还有内嵌脚本噪声。
const fixtureZdaye = `<!doctype html><html><body><ul class="lisd">
<li class="ul-cell"><p class="proxy_ip">116.63.130.30</p><p class="proxy_port">Port：30001<span class="copy-proxy"></span></p></li>
<li class="ul-cell"><p class="proxy_ip">8.215.112.240</p><p class="proxy_port">Port：8080</p></li>
<li class="ul-cell"><p class="proxy_ip"><script>var a=1.2.3.4;write(a);</script>8.137.13.191</p><p class="proxy_port">Port：9000</p></li>
<li class="page"><a href="/free/2/">第2页</a></li>
</ul></body></html>`

// TestHTMLTableExtractZdayeStyle li 行 + Port 标签单元格;导航 li 与脚本噪声不干扰。
func TestHTMLTableExtractZdayeStyle(t *testing.T) {
	got := htmlTableExtract([]byte(fixtureZdaye), "")
	want := []string{"116.63.130.30:30001", "8.215.112.240:8080", "8.137.13.191:9000"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("提取结果不符:\n got  %v\n want %v", got, want)
	}
}

// TestHTMLTableExtractFallsBackToFullText 无表格行时退回全文正则(89ip 的
// ip:port<br> 直排形态),且顺序保持原文出现序。
func TestHTMLTableExtractFallsBackToFullText(t *testing.T) {
	body := []byte(`<html><body><div class="wrap">154.65.39.7:80<br>140.249.90.211:8080<br>
	<script>var demo="9.9.9.9:9999";</script></div></body></html>`)
	got := htmlTableExtract(body, "")
	want := []string{"154.65.39.7:80", "140.249.90.211:8080"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("兜底提取结果不符:\n got  %v\n want %v", got, want)
	}
}

// TestHTMLTableExtractEmpty 挑战页/空页应产出 0 条且不 panic。
func TestHTMLTableExtractEmpty(t *testing.T) {
	for name, body := range map[string]string{
		"空白":     "<html></html>",
		"空字节":    "",
		"只有脚本":   "<html><body><script>var x=1;</script></body></html>",
		"残缺HTML": `<html><body><ul><li><p>116.63.130.30</p><p>Port：`,
	} {
		got := htmlTableExtract([]byte(body), "")
		if len(got) != 0 {
			t.Errorf("%s 页面不应产出条目,得到 %v", name, got)
		}
	}
}

// TestPortCandidatesSkipsDecimalsAndDates 端口候选要躲开小数与日期时间里的数字。
func TestPortCandidatesSkipsDecimalsAndDates(t *testing.T) {
	// "0.38秒" 抹小数后无候选;"09-27 15:00:01" 只剩低分候选,不该压过端口列。
	cands := portCandidatesIn("0.38秒")
	if len(cands) != 0 {
		t.Errorf("小数延迟不应产生端口候选: %v", cands)
	}
	cands = portCandidatesIn("09-27 15:00:01")
	for _, c := range cands {
		if c.score >= 3 {
			t.Errorf("日期时间里的数字不应是高置信端口: %+v", c)
		}
	}
	cands = portCandidatesIn("Port：30001")
	if len(cands) != 1 || cands[0].value != 30001 || cands[0].score != 3 {
		t.Errorf("Port 标签格应为高置信 30001: %+v", cands)
	}
	cands = portCandidatesIn("8080")
	if len(cands) != 1 || cands[0].value != 8080 || cands[0].score != 3 {
		t.Errorf("纯数字格应为高置信 8080: %+v", cands)
	}
}

// TestProxy5ExtractFiltersProtocol proxy5 JSON:按 protocols 过滤;截断的 JSON
// 尾部(没有 protocols 字段的残条目)不产生错协议条目。
func TestProxy5ExtractFiltersProtocol(t *testing.T) {
	// 末尾故意截断在半条记录上,模拟 8MB 上限截尾。
	body := []byte(`{"created_at_utc":"2026-09-27T15:00:16Z","count":5,"results":[
		{"ip_address":"103.179.252.170","port":3127,"protocols":["HTTP"],"country":"ID"},
		{"ip_address":"45.12.33.87","port":1080,"protocols":["SOCKS5"],"country":"DE"},
		{"ip_address":"188.165.200.9","port":3128,"protocols":["HTTP","HTTPS"],"country":"FR"},
		{"ip_address":"1.2.3.4","port":9999,"prot`)
	httpGot := proxy5APIExtract("http")(body, "")
	wantHTTP := []string{"103.179.252.170:3127", "188.165.200.9:3128"}
	if fmt.Sprint(httpGot) != fmt.Sprint(wantHTTP) {
		t.Fatalf("http 过滤结果不符:\n got  %v\n want %v", httpGot, wantHTTP)
	}
	s5Got := proxy5APIExtract("socks5")(body, "")
	wantS5 := []string{"45.12.33.87:1080"}
	if fmt.Sprint(s5Got) != fmt.Sprint(wantS5) {
		t.Fatalf("socks5 过滤结果不符:\n got  %v\n want %v", s5Got, wantS5)
	}
}

// TestProxy5ExtractLooseFallback 字段顺序变化导致三元组配不上时,退回 ip+port
// 宽松配对,不允许整源归零。
func TestProxy5ExtractLooseFallback(t *testing.T) {
	body := []byte(`{"results":[
		{"port":3127,"ip_address":"103.179.252.170","protocols":["HTTP"]},
		{"port":1080,"ip_address":"45.12.33.87","protocols":["SOCKS5"]}]}`)
	got := proxy5APIExtract("http")(body, "")
	want := []string{"103.179.252.170:3127", "45.12.33.87:1080"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("宽松配对结果不符:\n got  %v\n want %v", got, want)
	}
}

// TestFetchInjectsBrowserHeaders Fetch 应按源配置注入 UA 与附加头,默认源用 bot UA。
func TestFetchInjectsBrowserHeaders(t *testing.T) {
	var gotUA, gotLang, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotLang = r.Header.Get("Accept-Language")
		gotAccept = r.Header.Get("Accept")
		fmt.Fprint(w, "1.2.3.4:8080\n")
	}))
	defer srv.Close()

	// 配了浏览器头的源:UA 换成 Chrome,Accept-Language 注入,Accept 覆盖默认。
	def := &ProviderDef{
		Key: "t1", Title: "t1", DefaultURL: srv.URL,
		Protocol: "http", Region: "测试",
		UserAgent: chromeUA,
		Headers:   browserHeaders(),
		Extract:   linesExtract,
	}
	if _, err := def.Fetch(srv.Client(), SourceAuth{}, "", nil); err != nil {
		t.Fatalf("Fetch 失败: %v", err)
	}
	if gotUA != chromeUA {
		t.Errorf("User-Agent = %q, 期望 Chrome UA", gotUA)
	}
	if !strings.HasPrefix(gotLang, "zh-CN") {
		t.Errorf("Accept-Language = %q, 期望 zh-CN 前缀", gotLang)
	}
	if !strings.Contains(gotAccept, "text/html") {
		t.Errorf("Accept = %q, 应被源自带头覆盖为 html 形态", gotAccept)
	}

	// 未配置的源:保持默认 bot UA 与默认 Accept,API 源行为不变。
	gotUA, gotAccept = "", ""
	def2 := &ProviderDef{
		Key: "t2", Title: "t2", DefaultURL: srv.URL,
		Protocol: "http", Region: "测试",
		Extract: linesExtract,
	}
	if _, err := def2.Fetch(srv.Client(), SourceAuth{}, "", nil); err != nil {
		t.Fatalf("Fetch 失败: %v", err)
	}
	if gotUA != defaultFetchUA {
		t.Errorf("默认源 User-Agent = %q, 期望 %q", gotUA, defaultFetchUA)
	}
	if gotAccept != "*/*" {
		t.Errorf("默认源 Accept = %q, 期望 */*", gotAccept)
	}
}

// TestNewProviderDefsHaveBrowserUA 新接的 HTML 表格站必须带浏览器 UA:
// 忘配 UA 就是「注册了个永远 403 的源」,这里当契约钉死。
func TestNewProviderDefsHaveBrowserUA(t *testing.T) {
	for _, key := range []string{"zdaye", "ip89", "proxy5_http", "proxy5_socks5"} {
		def := ProviderByKey(key)
		if def == nil {
			t.Fatalf("源 %s 未注册", key)
		}
		if def.UserAgent != chromeUA {
			t.Errorf("源 %s 未配置浏览器 UA", key)
		}
		if def.Headers["Accept-Language"] == "" {
			t.Errorf("源 %s 缺少 Accept-Language 头", key)
		}
	}
}
