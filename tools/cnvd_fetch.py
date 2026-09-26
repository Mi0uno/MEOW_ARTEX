# -*- coding: utf-8 -*-
"""CNVD 漏洞列表采集器(CVE&CNVD 情报的独立采集代理)。

背景:CNVD 官网(cnvd.org.cn)对一切自动化访问启用加速乐两级 JS 挑战 + 验证码
(实测:纯 HTTP/正则解一阶段可过、二阶段环境检测过不去;headless 与常见隐身
补丁都会被验证码墙拦下)。因此本脚本采用业界标准做法——**持久化浏览器配置 +
首次人工过一次验证码,之后 cookie 长期有效、无人值守续跑**:

    1. 首次运行会弹出 Chrome 窗口(窗口在屏幕外,需要时拖回来);
    2. 若命中验证码,控制台会提示,人在窗口里把验证码点一下/拖一下即可;
    3. 通过后浏览器配置保存在 ./.cnvd-profile,重启脚本不再需要人工;
    4. 采集结果以 HTTP 服务形式提供给平台消费(见下)。

输出契约(与后端 cnvdFeedEntry 对齐,GET /cnvd.json):
    [{"cnvd": "CNVD-2026-123456", "title": "...", "severity": "高",
      "date": "2026-09-26", "description": "", "url": "https://www.cnvd.org.cn/flaw/show/..."}]

用法:
    pip install rebrowser-playwright        # 补丁版驱动(推荐,痕迹更少)
                                            # 或 pip install playwright
    python cnvd_fetch.py --serve 8930       # 常驻:服务 /cnvd.json,每 30 分钟重抓
    平台侧:情报源页签「CNVD 数据端点」填 http://127.0.0.1:8930/cnvd.json
    (非本机部署则填 http://<主机IP>:8930/cnvd.json;平台允许内网/环回地址)

注意:请控制抓取频率(默认单页 40 条、30 分钟一轮),尊重数据源。
"""

import argparse
import json
import os
import re
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LIST_URL = "https://www.cnvd.org.cn/flaw/list.htm?max=40&offset=0"
DATE_RE = re.compile(r"\d{4}-\d{2}-\d{2}")
ID_RE = re.compile(r"^CNVD-\d{4}-\d{4,7}$")
PROFILE_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), ".cnvd-profile")
CAPTCHA_WAIT_SEC = 300  # 人工验证码最长等待时间


def build_browser(pw, headed):
    """持久化配置 + 本机 Chrome(rebrowser 补丁版优先,痕迹更少)。"""
    for channel in ("chrome", "msedge", None):
        try:
            return pw.chromium.launch_persistent_context(
                PROFILE_DIR, channel=channel, headless=not headed,
                args=["--disable-blink-features=AutomationControlled",
                      "--window-position=-32000,-32000", "--window-size=1400,900"],
                locale="zh-CN",
                user_agent="Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
                           "(KHTML, like Gecko) Chrome/126.0 Safari/537.36",
                viewport={"width": 1366, "height": 850},
            )
        except Exception:
            continue
    raise SystemExit("未找到可用的 Chrome/Edge/Chromium。请安装 Chrome,或先 `playwright install chromium`。")


def is_captcha(page):
    try:
        return "验证码" in page.title()
    except Exception:
        return False


def wait_manual_captcha(page):
    """等待人工完成验证码(轮询标题/列表出现,超时放弃本轮)。"""
    print(">>> 命中验证码!请在弹出的 Chrome 窗口(屏幕外,可从任务栏点开)中完成验证;", f"等待最多 {CAPTCHA_WAIT_SEC} 秒…", flush=True)
    deadline = time.time() + CAPTCHA_WAIT_SEC
    while time.time() < deadline:
        time.sleep(3)
        try:
            if not is_captcha(page):
                print(">>> 验证码已通过,继续采集(浏览器配置已保存,下次无需人工)", flush=True)
                page.wait_for_timeout(3000)
                return True
        except Exception:
            pass
    print(">>> 验证码等待超时,本轮放弃(下次运行再试)", flush=True)
    return False


def scrape(pages, headed):
    """渲染列表页并解析为结构化列表。返回 JSON 契约数组。"""
    try:
        from rebrowser_playwright.sync_api import sync_playwright
    except ImportError:
        from playwright.sync_api import sync_playwright

    out, seen = [], set()
    with sync_playwright() as pw:
        ctx = build_browser(pw, headed)
        page = ctx.new_page()
        try:
            for i in range(pages):
                url = LIST_URL if i == 0 else f"https://www.cnvd.org.cn/flaw/list.htm?max=40&offset={40 * i}"
                page.goto(url, wait_until="commit", timeout=45000)
                page.wait_for_timeout(8000)  # 等加速乐挑战跳转与表格渲染
                if is_captcha(page):
                    if not headed:
                        raise SystemExit("headless 被验证码墙拦下:请加 --headed 重跑,按提示人工过一次验证码即可长期续跑。")
                    if not wait_manual_captcha(page):
                        return out
                links = page.evaluate('document.querySelectorAll(\'a[href*="flaw/show"]\').length')
                if links == 0:
                    page.wait_for_timeout(5000)  # 再等一拍,慢网兜底
                rows = page.evaluate(
                    """() => {
                        const out = [];
                        for (const a of document.querySelectorAll('a[href*="flaw/show"]')) {
                            const tr = a.closest('tr');
                            out.push({ href: a.href, id: a.innerText.trim(),
                                       cells: tr ? [...tr.querySelectorAll('td')].map(td => td.innerText.trim()) : [] });
                        }
                        return out;
                    }"""
                )
                for row in rows:
                    cnvd_id = (row.get("id") or "").upper()
                    if not ID_RE.match(cnvd_id) or cnvd_id in seen:
                        continue
                    cells = [c for c in row.get("cells", []) if c]
                    name, severity, date = "", "", ""
                    for c in cells:
                        if DATE_RE.fullmatch(c):
                            date = c
                        elif c in ("高", "中", "低", "高危", "中危", "低危"):
                            severity = c
                        elif not ID_RE.match(c) and len(c) > len(name):
                            name = c
                    if not name:
                        continue
                    seen.add(cnvd_id)
                    out.append({
                        "cnvd": cnvd_id,
                        "title": name,
                        "severity": severity,
                        "date": date,
                        "description": "",
                        "url": row.get("href", f"https://www.cnvd.org.cn/flaw/show/{cnvd_id}"),
                    })
        finally:
            ctx.close()
    return out


def main():
    ap = argparse.ArgumentParser(description="CNVD 漏洞列表采集器(供平台 vulndb_cnvd_url 消费)")
    ap.add_argument("--serve", type=int, required=True, metavar="PORT",
                    help="常驻 HTTP 服务模式,在指定端口提供 /cnvd.json")
    ap.add_argument("--interval", type=int, default=30, help="重抓间隔(分钟,默认 30)")
    ap.add_argument("--pages", type=int, default=1, help="抓取列表页数(默认 1 页 = 40 条)")
    ap.add_argument("--headed", action="store_true", help="用可见浏览器窗口运行(验证码需要人工时必须)")
    args = ap.parse_args()

    state = {"data": [], "ts": ""}

    def refresh():
        data = scrape(args.pages, args.headed)
        state["data"] = data
        state["ts"] = time.strftime("%Y-%m-%d %H:%M:%S")
        print(f"[{state['ts']}] 抓取 {len(data)} 条 CNVD", flush=True)

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path.rstrip("/") in ("/cnvd.json", ""):
                body = json.dumps(state["data"], ensure_ascii=False).encode("utf-8")
                self.send_response(200)
                self.send_header("Content-Type", "application/json; charset=utf-8")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)
            else:
                self.send_response(404)
                self.end_headers()

        def log_message(self, *a):
            pass

    refresh()
    server = ThreadingHTTPServer(("0.0.0.0", args.serve), Handler)
    print(f"服务 http://0.0.0.0:{args.serve}/cnvd.json(平台填 http://<本机IP>:{args.serve}/cnvd.json),"
          f"每 {args.interval} 分钟重抓", flush=True)
    threading.Thread(target=lambda: server.serve_forever(), daemon=True).start()
    while True:
        time.sleep(args.interval * 60)
        try:
            refresh()
        except Exception as e:
            print(f"重抓失败,下轮继续: {e}", flush=True)


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        sys.exit(0)
