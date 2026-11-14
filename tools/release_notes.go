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
		"<sub>**安装包选型与直连下载**（不确定芯片可在对应后台菜单查看；若选错系统会提示不匹配拒绝安装，换另一个即可）：</sub>",
		"",
		"| <sub>设备系统</sub> | <sub>芯片查看位置与对应规则</sub> | <sub>点击直接下载</sub> |",
		"| :--- | :--- | :--- |",
		fmt.Sprintf("| <sub>**飞牛 fnOS**</sub> | <sub>「设置→设备信息」看 CPU：ARM 盒子/NAS 选 `arm`；Intel/AMD 选 `x86`</sub> | <sub>[`fnos-arm.fpk`](%s/dkstore-fnos-arm-%s.fpk) · [`fnos-x86.fpk`](%s/dkstore-fnos-x86-%s.fpk)</sub> |", dlBase, ver, dlBase, ver),
		fmt.Sprintf("| <sub>**群晖 DSM 7+**</sub> | <sub>「控制面板→信息中心」：ARM 机型选 `aarch64`；Intel/AMD 选 `x86_64`</sub> | <sub>[`aarch64.spk`](%s/dkstore-aarch64-%s-0001.spk) · [`x86_64.spk`](%s/dkstore-x86_64-%s-0001.spk)</sub> |", dlBase, ver, dlBase, ver),
		fmt.Sprintf("| <sub>**iStoreOS / OpenWrt**</sub> | <sub>「状态→概览」看架构：64 位 ARM 选 `aarch64`，x86 选 `x86_64`（iStoreOS 选 `.run`）</sub> | <sub>iStoreOS: [`aarch64.run`](%s/dkstore-openwrt-aarch64-%s.run) · [`x86_64.run`](%s/dkstore-openwrt-x86_64-%s.run) ｜ ipk: [`arm64`](%s/luci-app-dkstore_%s-1_aarch64.ipk)/[`x86`](%s/luci-app-dkstore_%s-1_x86_64.ipk)/[`arm`](%s/luci-app-dkstore_%s-1_arm.ipk) ｜ apk: [`arm64`](%s/luci-app-dkstore-%s-r1-aarch64.apk)/[`x86`](%s/luci-app-dkstore-%s-r1-x86_64.apk)</sub> |", dlBase, ver, dlBase, ver, dlBase, ver, dlBase, ver, dlBase, ver, dlBase, ver, dlBase, ver),
		fmt.Sprintf("| <sub>**其他 NAS / 电脑**</sub> | <sub>任意 Docker 设备用镜像自动匹配芯片；Windows 电脑下 `.zip`；Linux 按 `uname -m`</sub> | <sub>`ghcr.io/%s:latest` · [`win-amd64.zip`](%s/dkstore-windows-amd64-%s.zip) · Linux: [`arm64`](%s/dkstore-linux-arm64-%s.tar.gz)/[`amd64`](%s/dkstore-linux-amd64-%s.tar.gz)/[`armv7`](%s/dkstore-linux-armv7-%s.tar.gz)</sub> |", repoLower, dlBase, ver, dlBase, ver, dlBase, ver, dlBase, ver),
	}, "\n")

	enGuide := strings.Join([]string{
		"<sub>**Package Selection & Direct Downloads** (check system info if unsure; mismatched packages are safely rejected):</sub>",
		"",
		"| <sub>Platform</sub> | <sub>How to Check Architecture</sub> | <sub>Direct Download</sub> |",
		"| :--- | :--- | :--- |",
		fmt.Sprintf("| <sub>**fnOS**</sub> | <sub>**Settings → Device Info**: ARM NAS/boxes → `arm`; Intel/AMD → `x86`</sub> | <sub>[`fnos-arm.fpk`](%s/dkstore-fnos-arm-%s.fpk) · [`fnos-x86.fpk`](%s/dkstore-fnos-x86-%s.fpk)</sub> |", dlBase, ver, dlBase, ver),
		fmt.Sprintf("| <sub>**Synology DSM 7+**</sub> | <sub>**Control Panel → Info Center**: ARM → `aarch64`; Intel/AMD → `x86_64`</sub> | <sub>[`aarch64.spk`](%s/dkstore-aarch64-%s-0001.spk) · [`x86_64.spk`](%s/dkstore-x86_64-%s-0001.spk)</sub> |", dlBase, ver, dlBase, ver),
		fmt.Sprintf("| <sub>**iStoreOS / OpenWrt**</sub> | <sub>**Status → Overview**: 64-bit ARM → `aarch64`, x86 → `x86_64` (`.run` for iStoreOS)</sub> | <sub>iStoreOS: [`aarch64.run`](%s/dkstore-openwrt-aarch64-%s.run) · [`x86_64.run`](%s/dkstore-openwrt-x86_64-%s.run) ｜ ipk: [`arm64`](%s/luci-app-dkstore_%s-1_aarch64.ipk)/[`x86`](%s/luci-app-dkstore_%s-1_x86_64.ipk)/[`arm`](%s/luci-app-dkstore_%s-1_arm.ipk) ｜ apk: [`arm64`](%s/luci-app-dkstore-%s-r1-aarch64.apk)/[`x86`](%s/luci-app-dkstore-%s-r1-x86_64.apk)</sub> |", dlBase, ver, dlBase, ver, dlBase, ver, dlBase, ver, dlBase, ver, dlBase, ver, dlBase, ver),
		fmt.Sprintf("| <sub>**Docker / PC / Linux**</sub> | <sub>Docker image auto-matches ARM/x86; Windows PC → `.zip`; Linux → `uname -m`</sub> | <sub>`ghcr.io/%s:latest` · [`win-amd64.zip`](%s/dkstore-windows-amd64-%s.zip) · Linux: [`arm64`](%s/dkstore-linux-arm64-%s.tar.gz)/[`amd64`](%s/dkstore-linux-amd64-%s.tar.gz)/[`armv7`](%s/dkstore-linux-armv7-%s.tar.gz)</sub> |", repoLower, dlBase, ver, dlBase, ver, dlBase, ver, dlBase, ver),
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
