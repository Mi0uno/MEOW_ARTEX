package vpscenter

// 内置工具集种子(照 promptcatalog/toolcatalog 惯例:代码持有种子,启动时幂等播种,
// 不写进 schema.sql)。三个内置集:
//
//	基础侦察 — nmap httpx nuclei subfinder whatweb
//	Web漏扫  — sqlmap ffuf gobuster nuclei
//	爆破破解 — hydra john hashcat
//
// 脚本原则:幂等可重跑。每步 command -v 判存在,apt 系走 apt-get install -y,
// Go 系走 go install @latest;单步失败 || true 兜底,不因一个工具断整批。
// 开头有 golang 兜底安装(if ! command -v go),apt 源里没有 golang 时退回 tarball。
// Go 装的二进制落在 $(go env GOPATH)/bin,统一前置进 PATH。

// BuiltinToolset 是一个内置工具集种子。
type BuiltinToolset struct {
	Name        string
	Description string
	Script      string
}

// golangBootstrap 是所有内置脚本共用的 Go 工具链兜底 + PATH 前置。
const golangBootstrap = `# golang 兜底:go install 需要 go 工具链
export DEBIAN_FRONTEND=noninteractive
export PATH="$PATH:/usr/local/go/bin:$HOME/go/bin:$(go env GOPATH 2>/dev/null)/bin"
if ! command -v go >/dev/null 2>&1; then
  apt-get update -y || true
  apt-get install -y golang-go || true
fi
if ! command -v go >/dev/null 2>&1; then
  # apt 没有 golang(老发行版):退回官方 tarball
  curl -fsSL https://go.dev/dl/go1.22.5.linux-amd64.tar.gz -o /tmp/go.tgz || true
  rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go.tgz || true
  rm -f /tmp/go.tgz
  export PATH="$PATH:/usr/local/go/bin"
fi
export GOPROXY=https://goproxy.cn,direct
`

// aptTool / goTool 生成单个工具的幂等安装步。
func aptTool(pkg, bin string) string {
	return "command -v " + bin + " >/dev/null 2>&1 || { apt-get install -y " + pkg + " || true; }\n"
}

func goTool(pkg, bin string) string {
	// 二进制落 $GOPATH/bin(默认 $HOME/go/bin),golangBootstrap 已把它加进 PATH。
	return "command -v " + bin + " >/dev/null 2>&1 || { go install " + pkg + " || true; }\n"
}

// BuiltinToolsets 返回三个内置工具集种子(调用方可安全修改返回切片内的脚本串)。
func BuiltinToolsets() []BuiltinToolset {
	recon := golangBootstrap +
		aptTool("nmap", "nmap") +
		aptTool("whatweb", "whatweb") +
		goTool("github.com/projectdiscovery/httpx/cmd/httpx@latest", "httpx") +
		goTool("github.com/projectdiscovery/nuclei/v3/cmd/nuclei@latest", "nuclei") +
		goTool("github.com/projectdiscovery/subfinder/v2/cmd/subfinder@latest", "subfinder") +
		"echo '基础侦察工具集安装完成'\n"

	webscan := golangBootstrap +
		aptTool("sqlmap", "sqlmap") +
		goTool("github.com/ffuf/ffuf/v2@latest", "ffuf") +
		goTool("github.com/OJ/gobuster/v3@latest", "gobuster") +
		goTool("github.com/projectdiscovery/nuclei/v3/cmd/nuclei@latest", "nuclei") +
		"echo 'Web 漏扫工具集安装完成'\n"

	brute := golangBootstrap +
		aptTool("hydra", "hydra") +
		aptTool("john", "john") +
		aptTool("hashcat", "hashcat") +
		"echo '爆破破解工具集安装完成'\n"

	return []BuiltinToolset{
		{
			Name:        "基础侦察",
			Description: "nmap / httpx / nuclei / subfinder / whatweb — 资产发现与指纹识别基线",
			Script:      recon,
		},
		{
			Name:        "Web漏扫",
			Description: "sqlmap / ffuf / gobuster / nuclei — Web 漏洞扫描与目录暴破",
			Script:      webscan,
		},
		{
			Name:        "爆破破解",
			Description: "hydra / john / hashcat — 口令爆破与哈希破解",
			Script:      brute,
		},
	}
}
