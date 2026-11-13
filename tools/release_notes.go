//go:build ignore

// release_notes.go 从仓库 CHANGELOG.md 中提取指定版本的中英双语更新说明（供 GitHub Actions Release 自动挂载）。
// 若指定版本尚未在 CHANGELOG.md 单独成节，优先回退提取 [Unreleased] 章节，其次回退按 Git 提交自动汇总。
//
// 用法：go run ./tools/release_notes.go -version v0.2.0 [-changelog CHANGELOG.md] [-out dist/RELEASE_NOTES.md]
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
	changelogFlag := flag.String("changelog", "CHANGELOG.md", "CHANGELOG 文件路径")
	outFlag := flag.String("out", "", "输出文件路径（留空则输出到 stdout）")
	flag.Parse()

	rawVer := strings.TrimSpace(*verFlag)
	if rawVer == "" {
		rawVer = strings.TrimSpace(os.Getenv("DKSTORE_VERSION"))
	}
	cleanVer := strings.TrimPrefix(rawVer, "v")
	tagVer := "v" + cleanVer

	body := ""
	if b, err := os.ReadFile(*changelogFlag); err == nil {
		body = extractChangelogSection(string(b), cleanVer)
	}
	if strings.TrimSpace(body) == "" {
		body = fallbackGitNotes(tagVer)
	}

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

func extractChangelogSection(content, cleanVer string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")

	// 1. 优先匹配精确版本标题：## [v0.2.0] / ## [0.2.0] / ## v0.2.0 / ## 0.2.0
	reExact := regexp.MustCompile(fmt.Sprintf(`^##\s+\[?v?%s\]?(?:\s|$|-)`, regexp.QuoteMeta(cleanVer)))
	if sec := sliceH2Section(lines, reExact); sec != "" {
		return sec
	}

	// 2. 回退匹配 ## [Unreleased] 章节（支持在提交信息写 release: vX.Y.Z 时直接合并 Unreleased 中维护的双语日志）
	reUnreleased := regexp.MustCompile(`(?i)^##\s+\[?unreleased\]?(?:\s|$|-)`)
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
