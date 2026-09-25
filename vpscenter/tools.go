package vpscenter

// 已知渗透工具的探测清单(~19 个)。DetectTools 用「command -v 判存在 +
// 版本命令 + 解析器」三件套探测;BuiltinToolsets 的安装脚本也按这份清单选型。
//
// 版本命令统一假设工具在 PATH 上;projectdiscovery 系与 ffuf 的版本输出走 stderr,
// 脚本里已统一 2>&1。解析器为空时用默认的「第一段版本号形状」正则。

// ToolProbe 是一个已知工具的探测定义。
type ToolProbe struct {
	// Name 是工具在 PATH 上的命令名(也是 vps_tool_detections.tool_name)。
	Name string
	// VersionCmd 是版本探测命令(输出前 3 行交给解析;为空则不探版本)。
	VersionCmd string
	// ParseVersion 从版本命令输出提取版本串;nil 时用默认正则。
	ParseVersion func(out string) string
}

// KnownTools 返回全部已知工具的探测清单(顺序稳定,供检测矩阵展示)。
func KnownTools() []ToolProbe {
	return []ToolProbe{
		{Name: "nmap", VersionCmd: "nmap --version"},
		{Name: "masscan", VersionCmd: "masscan --version"},
		{Name: "nuclei", VersionCmd: "nuclei -version"},
		{Name: "httpx", VersionCmd: "httpx -version"},
		{Name: "subfinder", VersionCmd: "subfinder -version"},
		{Name: "sqlmap", VersionCmd: "sqlmap --version"},
		{Name: "ffuf", VersionCmd: "ffuf -V"},
		{Name: "gobuster", VersionCmd: "gobuster version"},
		{Name: "dirsearch", VersionCmd: "dirsearch --version"},
		{Name: "hydra", VersionCmd: "hydra -h"},
		{Name: "john", VersionCmd: "john --version"},
		{Name: "hashcat", VersionCmd: "hashcat --version"},
		{Name: "whatweb", VersionCmd: "whatweb --version"},
		{Name: "wafw00f", VersionCmd: "wafw00f --version"},
		{Name: "amass", VersionCmd: "amass -version"},
		{Name: "curl", VersionCmd: "curl --version"},
		{Name: "python3", VersionCmd: "python3 --version"},
		{Name: "git", VersionCmd: "git --version"},
		{Name: "dig", VersionCmd: "dig -v"},
	}
}

// toolProbes 是 name → 探测定义的索引(包加载时建好,只读)。
var toolProbes = func() map[string]ToolProbe {
	m := make(map[string]ToolProbe)
	for _, p := range KnownTools() {
		m[p.Name] = p
	}
	return m
}()

// ToolProbeByName 取单个工具的探测定义(未知工具返回 nil)。
func ToolProbeByName(name string) *ToolProbe {
	if p, ok := toolProbes[name]; ok {
		return &p
	}
	return nil
}

// KnownToolNames 返回全部已知工具名(与 KnownTools 同序)。
func KnownToolNames() []string {
	names := make([]string, 0, len(toolProbes))
	for _, p := range KnownTools() {
		names = append(names, p.Name)
	}
	return names
}
