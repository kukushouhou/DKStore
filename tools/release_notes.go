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
		"<sub><strong>安装包选型与直连下载</strong>（不确定芯片可按左侧路径在设备后台查看；若选错包系统会提示不匹配拒绝安装，换另一行即可）：</sub>",
		"",
		"<table>",
		"  <thead>",
		"    <tr>",
		"      <th align=\"left\"><sub>设备系统（后台查看位置）</sub></th>",
		"      <th align=\"left\"><sub>CPU / 架构类型</sub></th>",
		"      <th align=\"left\"><sub>对应安装包（点击直接下载）</sub></th>",
		"    </tr>",
		"  </thead>",
		"  <tbody>",
		fmt.Sprintf("    <tr><td rowspan=\"2\"><sub><strong>飞牛 fnOS</strong><br>设置 → 设备信息</sub></td><td><sub><strong>ARM 64位</strong>（ARM 成品 NAS、RK3588/3568、OEC 等盒子）</sub></td><td><sub><a href=\"%s/dkstore-fnos-arm-%s.fpk\"><code>dkstore-fnos-arm-%s.fpk</code></a></sub></td></tr>", dlBase, ver, ver),
		fmt.Sprintf("    <tr><td><sub><strong>x86 64位</strong>（Intel / AMD 小主机、电脑组装机）</sub></td><td><sub><a href=\"%s/dkstore-fnos-x86-%s.fpk\"><code>dkstore-fnos-x86-%s.fpk</code></a></sub></td></tr>", dlBase, ver, ver),
		fmt.Sprintf("    <tr><td rowspan=\"2\"><sub><strong>群晖 DSM 7.0+</strong><br>控制面板 → 信息中心</sub></td><td><sub><strong>ARM 64位</strong>（DS223 / DS124 / j 系列等 Realtek 机型）</sub></td><td><sub><a href=\"%s/dkstore-aarch64-%s-0001.spk\"><code>dkstore-aarch64-%s-0001.spk</code></a></sub></td></tr>", dlBase, ver, ver),
		fmt.Sprintf("    <tr><td><sub><strong>x86 64位</strong>（DS220+ / DS423+ / DS920+ 等 Intel/AMD）</sub></td><td><sub><a href=\"%s/dkstore-x86_64-%s-0001.spk\"><code>dkstore-x86_64-%s-0001.spk</code></a></sub></td></tr>", dlBase, ver, ver),
		fmt.Sprintf("    <tr><td rowspan=\"3\"><sub><strong>iStoreOS / OpenWrt</strong><br>状态 → 概览</sub></td><td><sub><strong>ARM 64位 (aarch64)</strong>（R2S / R4S / R5S / N1 等 64位路由盒子）</sub></td><td><sub>iStoreOS: <a href=\"%s/dkstore-openwrt-aarch64-%s.run\"><code>aarch64.run</code></a> · OpenWrt: <a href=\"%s/luci-app-dkstore_%s-1_aarch64.ipk\"><code>ipk</code></a> / <a href=\"%s/luci-app-dkstore-%s-r1-aarch64.apk\"><code>apk</code></a></sub></td></tr>", dlBase, ver, dlBase, ver, dlBase, ver),
		fmt.Sprintf("    <tr><td><sub><strong>x86 64位 (x86_64)</strong>（N100 / J4125 软路由、虚拟机）</sub></td><td><sub>iStoreOS: <a href=\"%s/dkstore-openwrt-x86_64-%s.run\"><code>x86_64.run</code></a> · OpenWrt: <a href=\"%s/luci-app-dkstore_%s-1_x86_64.ipk\"><code>ipk</code></a> / <a href=\"%s/luci-app-dkstore-%s-r1-x86_64.apk\"><code>apk</code></a></sub></td></tr>", dlBase, ver, dlBase, ver, dlBase, ver),
		fmt.Sprintf("    <tr><td><sub><strong>ARM 32位 (arm)</strong>（老款 32 位 ARM 路由器）</sub></td><td><sub>OpenWrt: <a href=\"%s/luci-app-dkstore_%s-1_arm.ipk\"><code>luci-app-dkstore_%s-1_arm.ipk</code></a></sub></td></tr>", dlBase, ver, ver),
		fmt.Sprintf("    <tr><td rowspan=\"3\"><sub><strong>其他 NAS / 电脑</strong><br>Docker / 独立运行</sub></td><td><sub><strong>自动匹配芯片（Docker）</strong>（绿联 / 极空间 / 威联通 / Unraid / Linux）</sub></td><td><sub><code>ghcr.io/%s:latest</code></sub></td></tr>", repoLower),
		fmt.Sprintf("    <tr><td><sub><strong>Linux 命令行二进制</strong>（<code>uname -m</code> 查看架构）</sub></td><td><sub><a href=\"%s/dkstore-linux-arm64-%s.tar.gz\"><code>arm64.tar.gz</code></a> · <a href=\"%s/dkstore-linux-amd64-%s.tar.gz\"><code>amd64.tar.gz</code></a> · <a href=\"%s/dkstore-linux-armv7-%s.tar.gz\"><code>armv7.tar.gz</code></a></sub></td></tr>", dlBase, ver, dlBase, ver, dlBase, ver),
		fmt.Sprintf("    <tr><td><sub><strong>Windows 64位电脑</strong>（Win 10 / 11 解压双击运行）</sub></td><td><sub><a href=\"%s/dkstore-windows-amd64-%s.zip\"><code>dkstore-windows-amd64-%s.zip</code></a></sub></td></tr>", dlBase, ver, ver),
		"  </tbody>",
		"</table>",
	}, "\n")

	enGuide := strings.Join([]string{
		"<sub><strong>Package Selection & Direct Downloads</strong> (check system info on the left if unsure; mismatched packages are safely rejected):</sub>",
		"",
		"<table>",
		"  <thead>",
		"    <tr>",
		"      <th align=\"left\"><sub>Platform (Where to Check)</sub></th>",
		"      <th align=\"left\"><sub>CPU / Architecture</sub></th>",
		"      <th align=\"left\"><sub>Direct Download</sub></th>",
		"    </tr>",
		"  </thead>",
		"  <tbody>",
		fmt.Sprintf("    <tr><td rowspan=\"2\"><sub><strong>fnOS</strong><br>Settings → Device Info</sub></td><td><sub><strong>ARM 64-bit</strong> (ARM NAS, RK3588/3568, OEC / ARM boxes)</sub></td><td><sub><a href=\"%s/dkstore-fnos-arm-%s.fpk\"><code>dkstore-fnos-arm-%s.fpk</code></a></sub></td></tr>", dlBase, ver, ver),
		fmt.Sprintf("    <tr><td><sub><strong>x86 64-bit</strong> (Intel / AMD mini PCs, DIY PCs)</sub></td><td><sub><a href=\"%s/dkstore-fnos-x86-%s.fpk\"><code>dkstore-fnos-x86-%s.fpk</code></a></sub></td></tr>", dlBase, ver, ver),
		fmt.Sprintf("    <tr><td rowspan=\"2\"><sub><strong>Synology DSM 7.0+</strong><br>Control Panel → Info Center</sub></td><td><sub><strong>ARM 64-bit</strong> (DS223 / DS124 / j-series Realtek models)</sub></td><td><sub><a href=\"%s/dkstore-aarch64-%s-0001.spk\"><code>dkstore-aarch64-%s-0001.spk</code></a></sub></td></tr>", dlBase, ver, ver),
		fmt.Sprintf("    <tr><td><sub><strong>x86 64-bit</strong> (DS220+ / DS423+ / DS920+ Intel/AMD)</sub></td><td><sub><a href=\"%s/dkstore-x86_64-%s-0001.spk\"><code>dkstore-x86_64-%s-0001.spk</code></a></sub></td></tr>", dlBase, ver, ver),
		fmt.Sprintf("    <tr><td rowspan=\"3\"><sub><strong>iStoreOS / OpenWrt</strong><br>Status → Overview</sub></td><td><sub><strong>ARM 64-bit (aarch64)</strong> (R2S / R4S / R5S / N1 / 64-bit ARM routers)</sub></td><td><sub>iStoreOS: <a href=\"%s/dkstore-openwrt-aarch64-%s.run\"><code>aarch64.run</code></a> · OpenWrt: <a href=\"%s/luci-app-dkstore_%s-1_aarch64.ipk\"><code>ipk</code></a> / <a href=\"%s/luci-app-dkstore-%s-r1-aarch64.apk\"><code>apk</code></a></sub></td></tr>", dlBase, ver, dlBase, ver, dlBase, ver),
		fmt.Sprintf("    <tr><td><sub><strong>x86 64-bit (x86_64)</strong> (N100 / J4125 routers, VMs)</sub></td><td><sub>iStoreOS: <a href=\"%s/dkstore-openwrt-x86_64-%s.run\"><code>x86_64.run</code></a> · OpenWrt: <a href=\"%s/luci-app-dkstore_%s-1_x86_64.ipk\"><code>ipk</code></a> / <a href=\"%s/luci-app-dkstore-%s-r1-x86_64.apk\"><code>apk</code></a></sub></td></tr>", dlBase, ver, dlBase, ver, dlBase, ver),
		fmt.Sprintf("    <tr><td><sub><strong>ARM 32-bit (arm)</strong> (legacy 32-bit ARM routers)</sub></td><td><sub>OpenWrt: <a href=\"%s/luci-app-dkstore_%s-1_arm.ipk\"><code>luci-app-dkstore_%s-1_arm.ipk</code></a></sub></td></tr>", dlBase, ver, ver),
		fmt.Sprintf("    <tr><td rowspan=\"3\"><sub><strong>Other NAS / PC</strong><br>Docker / Standalone</sub></td><td><sub><strong>Auto-Match CPU (Docker)</strong> (UGREEN / ZSpace / QNAP / Unraid / Linux)</sub></td><td><sub><code>ghcr.io/%s:latest</code></sub></td></tr>", repoLower),
		fmt.Sprintf("    <tr><td><sub><strong>Linux Standalone Binary</strong> (check via <code>uname -m</code>)</sub></td><td><sub><a href=\"%s/dkstore-linux-arm64-%s.tar.gz\"><code>arm64.tar.gz</code></a> · <a href=\"%s/dkstore-linux-amd64-%s.tar.gz\"><code>amd64.tar.gz</code></a> · <a href=\"%s/dkstore-linux-armv7-%s.tar.gz\"><code>armv7.tar.gz</code></a></sub></td></tr>", dlBase, ver, dlBase, ver, dlBase, ver),
		fmt.Sprintf("    <tr><td><sub><strong>Windows 64-bit PC</strong> (Win 10 / 11 extract & run)</sub></td><td><sub><a href=\"%s/dkstore-windows-amd64-%s.zip\"><code>dkstore-windows-amd64-%s.zip</code></a></sub></td></tr>", dlBase, ver, ver),
		"  </tbody>",
		"</table>",
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
