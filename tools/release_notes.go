//go:build ignore

// release_notes.go 从仓库 CHANGELOG.md 中提取指定版本的中英双语更新说明（自动附带普通用户安装包选型直连下载表与 Commit 超链接，供 GitHub Actions Release 自动挂载）。
// 若指定版本尚未在 CHANGELOG.md 单独成节，优先回退提取 [Unreleased] 章节，其次回退按 Git 提交自动汇总。
//
// 用法：go run ./tools/release_notes.go -version v0.2.0 [-repo kukushouhou/DKStore] [-changelog CHANGELOG.md] [-out dist/RELEASE_NOTES.md]
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

func main() {
	verFlag := flag.String("version", "", "目标版本号（如 v0.2.0 或 0.2.0）")
	repoFlag := flag.String("repo", "", "GitHub 仓库路径（如 kukushouhou/DKStore）")
	changelogFlag := flag.String("changelog", "CHANGELOG.md", "CHANGELOG 文件路径")
	outFlag := flag.String("out", "", "输出文件路径（留空则输出到 stdout）")
	flag.Parse()

	rawVer := strings.TrimSpace(*verFlag)
	if rawVer == "" {
		rawVer = strings.TrimSpace(os.Getenv("DKSTORE_VERSION"))
	}
	cleanVer := strings.TrimPrefix(rawVer, "v")
	if cleanVer == "" {
		cleanVer = "0.2.0"
	}
	tagVer := "v" + cleanVer

	repo := strings.TrimSpace(*repoFlag)
	if repo == "" {
		repo = strings.TrimSpace(os.Getenv("GITHUB_REPOSITORY"))
	}
	if repo == "" {
		repo = "kukushouhou/DKStore"
	}

	body := ""
	if b, err := os.ReadFile(*changelogFlag); err == nil {
		body = extractChangelogSection(string(b), cleanVer)
	}
	if strings.TrimSpace(body) == "" {
		body = fallbackGitNotes(tagVer)
	}

	body = linkifyCommitHashes(body, repo)
	body = attachDownloadGuide(body, repo, cleanVer)

	if *outFlag != "" {
		_ = os.MkdirAll(filepath.Dir(*outFlag), 0o755)
		if err := os.WriteFile(*outFlag, []byte(body+"\n"), 0o644); err != nil {
			panic(err)
		}
		fmt.Printf("Extracted release notes for %s -> %s (%d bytes)\n", tagVer, *outFlag, len(body))
		return
	}
	fmt.Println(body)
}

// linkifyCommitHashes 将尚未包裹在 Markdown 链接中的行内短哈希（如 `389d6d6`）自动补全为 Commit 超链接。
func linkifyCommitHashes(text, repo string) string {
	reCodeHash := regexp.MustCompile("`([0-9a-f]{7,40})`")
	matches := reCodeHash.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	var out strings.Builder
	last := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		hash := text[m[2]:m[3]]
		out.WriteString(text[last:start])
		// 若前后已是 [`hash`](...) 结构则保持原样，避免重复嵌套
		alreadyLinked := start > 0 && text[start-1] == '[' && end < len(text) && text[end] == ']'
		if alreadyLinked {
			out.WriteString(text[start:end])
		} else {
			fmt.Fprintf(&out, "[`%s`](https://github.com/%s/commit/%s)", hash, repo, hash)
		}
		last = end
	}
	out.WriteString(text[last:])
	return out.String()
}

// attachDownloadGuide 在中英文日志段落上方分别注入客观对等的安装包选型与直连下载指南表。
func attachDownloadGuide(body, repo, ver string) string {
	dlBase := fmt.Sprintf("https://github.com/%s/releases/download/v%s", repo, ver)
	repoLower := strings.ToLower(repo)

	zhGuide := strings.Join([]string{
		"### 安装包怎么选（点击文件名直接下载）",
		"",
		"请根据您的设备系统与芯片类型选择对应安装包（若不确定芯片，可按表中路径在设备后台查看；即使选错，系统也会提示架构不匹配而拒绝安装，换另一个包即可，不会影响设备）：",
		"",
		"| 设备系统 | 怎么确认选哪一个？ | 点击直接下载安装包 |",
		"| :--- | :--- | :--- |",
		fmt.Sprintf("| **飞牛 fnOS**<br>（应用中心 → 手动安装） | 在飞牛「设置 → 设备信息」查看 CPU：<br>• **ARM 设备**（ARM 成品 NAS、RK3588/RK3568、OEC 等 ARM 盒子）选 `fnos-arm`<br>• **x86 设备**（Intel / AMD 小主机、电脑组装机）选 `fnos-x86` | • [`dkstore-fnos-arm-%s.fpk`](%s/dkstore-fnos-arm-%s.fpk)<br>• [`dkstore-fnos-x86-%s.fpk`](%s/dkstore-fnos-x86-%s.fpk) |", ver, dlBase, ver, ver, dlBase, ver),
		fmt.Sprintf("| **群晖 Synology NAS**<br>（DSM 7.0+ 套件中心 → 手动安装） | 在群晖「控制面板 → 信息中心」查看处理器：<br>• **ARM 处理器**（如 DS223 / DS124 / j 系列等 Realtek 机型）选 `aarch64`<br>• **Intel / AMD 处理器**（如 DS220+ / DS423+ / DS920+ 等）选 `x86_64` | • [`dkstore-aarch64-%s-0001.spk`](%s/dkstore-aarch64-%s-0001.spk)<br>• [`dkstore-x86_64-%s-0001.spk`](%s/dkstore-x86_64-%s-0001.spk) |", ver, dlBase, ver, ver, dlBase, ver),
		fmt.Sprintf("| **iStoreOS / OpenWrt 软路由** | 在后台「状态 → 概览」查看架构（iStoreOS 选 `.run` 在 iStore 手动安装；OpenWrt 选 `.ipk` 或新版 `.apk`）：<br>• **64 位 ARM**（R2S / R4S / R5S / N1 / ARM 路由与盒子）选 `aarch64`；32 位旧路由选 `arm`<br>• **x86 软路由 / 虚拟机**选 `x86_64` | • **iStoreOS 一键包**：[`aarch64.run`](%s/dkstore-openwrt-aarch64-%s.run) · [`x86_64.run`](%s/dkstore-openwrt-x86_64-%s.run)<br>• **OpenWrt `.ipk`**：[`aarch64`](%s/luci-app-dkstore_%s-1_aarch64.ipk) · [`x86_64`](%s/luci-app-dkstore_%s-1_x86_64.ipk) · [`arm`](%s/luci-app-dkstore_%s-1_arm.ipk)<br>• **OpenWrt `.apk`**：[`aarch64`](%s/luci-app-dkstore-%s-r1-aarch64.apk) · [`x86_64`](%s/luci-app-dkstore-%s-r1-x86_64.apk) |", dlBase, ver, dlBase, ver, dlBase, ver, dlBase, ver, dlBase, ver, dlBase, ver, dlBase, ver),
		fmt.Sprintf("| **绿联 / 极空间 / 威联通 / Linux / Windows** | • **Docker 部署（免区分芯片）**：任意支持 Docker 的 NAS 或主机直接使用镜像，Docker 会自动匹配 ARM / x86<br>• **Windows 10/11 电脑**：下载 `.zip` 解压运行 `dkstore.exe`<br>• **Linux 独立二进制**：按 `uname -m` 选对应 `.tar.gz` | • Docker：`ghcr.io/%s:latest`<br>• Windows：[`dkstore-windows-amd64-%s.zip`](%s/dkstore-windows-amd64-%s.zip)<br>• Linux：[`arm64`](%s/dkstore-linux-arm64-%s.tar.gz) · [`amd64`](%s/dkstore-linux-amd64-%s.tar.gz) · [`armv7`](%s/dkstore-linux-armv7-%s.tar.gz) |", repoLower, ver, dlBase, ver, dlBase, ver, dlBase, ver, dlBase, ver),
	}, "\n")

	enGuide := strings.Join([]string{
		"### Package Selection & Direct Downloads",
		"",
		"Choose the installer matching your platform and CPU architecture (check the system info path below if unsure; mismatched packages are safely rejected by the installer without affecting your device):",
		"",
		"| Platform | How to Check Your Architecture | Direct Download |",
		"| :--- | :--- | :--- |",
		fmt.Sprintf("| **fnOS**<br>(App Center → Manual Install) | Check CPU in fnOS **Settings → Device Info**:<br>• **ARM devices** (ARM NAS, RK3588/RK3568, OEC / ARM boxes) → `fnos-arm`<br>• **x86 devices** (Intel / AMD mini PCs, DIY PCs) → `fnos-x86` | • [`dkstore-fnos-arm-%s.fpk`](%s/dkstore-fnos-arm-%s.fpk)<br>• [`dkstore-fnos-x86-%s.fpk`](%s/dkstore-fnos-x86-%s.fpk) |", ver, dlBase, ver, ver, dlBase, ver),
		fmt.Sprintf("| **Synology NAS**<br>(DSM 7.0+ Package Center → Manual Install) | Check CPU in DSM **Control Panel → Info Center**:<br>• **ARM CPUs** (e.g., DS223 / DS124 / j-series) → `aarch64`<br>• **Intel / AMD CPUs** (e.g., DS220+ / DS423+ / DS920+) → `x86_64` | • [`dkstore-aarch64-%s-0001.spk`](%s/dkstore-aarch64-%s-0001.spk)<br>• [`dkstore-x86_64-%s-0001.spk`](%s/dkstore-x86_64-%s-0001.spk) |", ver, dlBase, ver, ver, dlBase, ver),
		fmt.Sprintf("| **iStoreOS / OpenWrt Routers** | Check architecture in **Status → Overview** (`.run` for iStoreOS Manual Install; `.ipk` for `opkg`; `.apk` for OpenWrt `apk`):<br>• **64-bit ARM** (R2S / R4S / R5S / N1 / ARM routers) → `aarch64`; 32-bit ARM → `arm`<br>• **x86 routers / VMs** → `x86_64` | • **iStoreOS `.run`**: [`aarch64.run`](%s/dkstore-openwrt-aarch64-%s.run) · [`x86_64.run`](%s/dkstore-openwrt-x86_64-%s.run)<br>• **OpenWrt `.ipk`**: [`aarch64`](%s/luci-app-dkstore_%s-1_aarch64.ipk) · [`x86_64`](%s/luci-app-dkstore_%s-1_x86_64.ipk) · [`arm`](%s/luci-app-dkstore_%s-1_arm.ipk)<br>• **OpenWrt `.apk`**: [`aarch64`](%s/luci-app-dkstore-%s-r1-aarch64.apk) · [`x86_64`](%s/luci-app-dkstore-%s-r1-x86_64.apk) |", dlBase, ver, dlBase, ver, dlBase, ver, dlBase, ver, dlBase, ver, dlBase, ver, dlBase, ver),
		fmt.Sprintf("| **Docker / Linux / Windows** | • **Docker (auto-detects ARM / x86)**: works on UGREEN, ZSpace, QNAP, Unraid, and any Docker host<br>• **Windows 10/11 PC**: extract `.zip` and run `dkstore.exe`<br>• **Linux standalone**: match `uname -m` | • Docker: `ghcr.io/%s:latest`<br>• Windows: [`dkstore-windows-amd64-%s.zip`](%s/dkstore-windows-amd64-%s.zip)<br>• Linux: [`arm64`](%s/dkstore-linux-arm64-%s.tar.gz) · [`amd64`](%s/dkstore-linux-amd64-%s.tar.gz) · [`armv7`](%s/dkstore-linux-armv7-%s.tar.gz) |", repoLower, ver, dlBase, ver, dlBase, ver, dlBase, ver, dlBase, ver),
	}, "\n")

	parts := strings.SplitN(body, "\n---\n", 2)
	if len(parts) == 2 {
		return zhGuide + "\n\n" + strings.TrimSpace(parts[0]) + "\n\n---\n\n" + enGuide + "\n\n" + strings.TrimSpace(parts[1])
	}
	return zhGuide + "\n\n" + strings.TrimSpace(body) + "\n\n---\n\n" + enGuide
}

func extractChangelogSection(content, cleanVer string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")

	// 1. 优先匹配精确版本标题：## [v0.2.0] / ## [0.2.0] / ## v0.2.0 / ## 0.2.0
	reExact := regexp.MustCompile(fmt.Sprintf(`^##\s+\[?v?%s\]?(?:\s|$|-)`, regexp.QuoteMeta(cleanVer)))
	if sec := sliceH2Section(lines, reExact); sec != "" {
		return sec
	}

	// 2. 回退匹配 ## [开发中 / Unreleased] 或 ## [Unreleased] 章节（支持发版时直接提取未发布双语日志）
	reUnreleased := regexp.MustCompile(`(?i)^##\s+\[?(?:开发中\s*/\s*)?unreleased\]?(?:\s|$|-)|^##\s+\[?开发中\]?(?:\s|$|-)`)
	return sliceH2Section(lines, reUnreleased)
}

func sliceH2Section(lines []string, headerRe *regexp.Regexp) string {
	inSec := false
	var buf []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inSec {
			if headerRe.MatchString(trimmed) {
				inSec = true
			}
			continue
		}
		if strings.HasPrefix(trimmed, "## ") {
			break
		}
		buf = append(buf, line)
	}
	return strings.TrimSpace(strings.Join(buf, "\n"))
}

func fallbackGitNotes(currentTag string) string {
	tagsOut, _ := exec.Command("git", "tag", "--sort=-creatordate").Output()
	var prevTag string
	for _, t := range strings.Split(strings.TrimSpace(string(tagsOut)), "\n") {
		t = strings.TrimSpace(t)
		if t != "" && t != currentTag && strings.HasPrefix(t, "v") {
			prevTag = t
			break
		}
	}
	revRange := "HEAD"
	if prevTag != "" {
		revRange = prevTag + "..HEAD"
	}
	logOut, _ := exec.Command("git", "log", revRange, "--no-merges", "--pretty=format:- %s (`%h`)").Output()
	items := strings.TrimSpace(string(logOut))
	if items == "" {
		items = "- Initial release"
	}
	return "### Changes / 更新内容\n\n" + items
}
