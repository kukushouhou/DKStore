// Package rules 嵌入并提供数据库识别、标签规则与常见仓库链接映射。
package rules

import (
	"embed"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed *.yaml
var rulesFS embed.FS

// DatabaseRule 定义一种数据库或有状态中间件的识别与系列判断规则。
type DatabaseRule struct {
	Name    string   `yaml:"name"`
	Kind    string   `yaml:"kind"` // "database" | "mq" | "middleware"（缺省为 "database"）
	Images  []string `yaml:"images"`
	Pattern string   `yaml:"pattern"`
	Series  string   `yaml:"series"` // "major" | "minor"
	Docs    string   `yaml:"docs"`
	re      *regexp.Regexp
}

// RepoLink 定义容器/镜像关联的源码仓库、发行说明、官方网站、官方文档与镜像仓库链接。
type RepoLink struct {
	Repo        string `yaml:"repo" json:"repo,omitempty"`
	Releases    string `yaml:"releases" json:"releases,omitempty"`
	Website     string `yaml:"website" json:"website,omitempty"`
	Docs        string `yaml:"docs" json:"docs,omitempty"`
	RegistryURL string `yaml:"registryUrl" json:"registryUrl,omitempty"`
}

// Set 汇总所有已加载的规则。
type Set struct {
	Databases          []DatabaseRule
	RollingTags        map[string]bool
	PrereleaseKeywords []string
	Links              map[string]RepoLink
}

var (
	once    sync.Once
	defSet  *Set
	loadErr error
)

// Default 返回内嵌的默认规则集。
func Default() *Set {
	once.Do(func() {
		defSet, loadErr = load()
		if loadErr != nil {
			panic(fmt.Sprintf("加载内嵌规则失败: %v", loadErr))
		}
	})
	return defSet
}

func load() (*Set, error) {
	s := &Set{
		RollingTags: make(map[string]bool),
		Links:       make(map[string]RepoLink),
	}

	var dbFile struct {
		Databases []DatabaseRule `yaml:"databases"`
	}
	b, err := rulesFS.ReadFile("databases.yaml")
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(b, &dbFile); err != nil {
		return nil, err
	}
	for i := range dbFile.Databases {
		if strings.TrimSpace(dbFile.Databases[i].Kind) == "" {
			dbFile.Databases[i].Kind = "database"
		}
		if p := dbFile.Databases[i].Pattern; p != "" {
			re, err := regexp.Compile("(?i)" + p)
			if err != nil {
				return nil, err
			}
			dbFile.Databases[i].re = re
		}
	}
	s.Databases = dbFile.Databases

	var tagFile struct {
		RollingTags        []string `yaml:"rolling_tags"`
		PrereleaseKeywords []string `yaml:"prerelease_keywords"`
	}
	b, err = rulesFS.ReadFile("tag_patterns.yaml")
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(b, &tagFile); err != nil {
		return nil, err
	}
	for _, t := range tagFile.RollingTags {
		s.RollingTags[strings.ToLower(t)] = true
	}
	s.PrereleaseKeywords = tagFile.PrereleaseKeywords

	var linkFile struct {
		Links map[string]RepoLink `yaml:"links"`
	}
	b, err = rulesFS.ReadFile("repo_links.yaml")
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(b, &linkFile); err != nil {
		return nil, err
	}
	for k, v := range linkFile.Links {
		s.Links[strings.ToLower(k)] = v
	}
	return s, nil
}

// MatchDatabase 判断仓库名（如 library/mysql 或 ghcr.io/org/postgres）是否属于数据库镜像。
// exact=true 表示精确名单命中，exact=false 表示由正则兜底命中（疑似数据库）。
func (s *Set) MatchDatabase(registry, repository string) (rule *DatabaseRule, exact bool) {
	repoLower := strings.ToLower(repository)
	fullLower := strings.ToLower(registry + "/" + repository)
	for i := range s.Databases {
		d := &s.Databases[i]
		for _, img := range d.Images {
			imgLower := strings.ToLower(img)
			if repoLower == imgLower || fullLower == imgLower {
				return d, true
			}
		}
	}
	for i := range s.Databases {
		d := &s.Databases[i]
		if d.re != nil && d.re.MatchString(repoLower) {
			return d, false
		}
	}
	return nil, false
}

// IsRollingTag 判断标签是否为 latest / stable 等滚动标签。
func (s *Set) IsRollingTag(tag string) bool {
	return s.RollingTags[strings.ToLower(strings.TrimSpace(tag))]
}

// ResolveLinks 根据 registry 与 repository 生成官方仓库和更新说明链接。
func (s *Set) ResolveLinks(registry, repository, targetVer string) RepoLink {
	return s.ResolveContainerLinks(registry, repository, nil, targetVer)
}

// ResolveContainerLinks 结合容器/镜像 OCI 标签、内置规则与镜像仓库信息解析完整链接集合：
//   - 识别源码仓库（如 GitHub/GitLab/Gitee/Codeberg）仅用于额外自动补齐「发行说明」(Releases)，绝不隐藏其他有效链接；
//   - 容器标签自带的官方网站（Website）、官方文档（Docs）、源码仓库（Repo）与镜像仓库页（RegistryURL）只要地址不重复均独立保留；
//   - 自动过滤基础系统/运行时镜像（如 alpine/debian/ubuntu/python/node）遗留的父镜像标签，避免串台误判。
func (s *Set) ResolveContainerLinks(registry, repository string, labels map[string]string, _ string) RepoLink {
	regLower := strings.ToLower(strings.TrimSpace(registry))
	repoClean := strings.TrimSpace(repository)
	repoLower := strings.ToLower(repoClean)
	fullLower := regLower + "/" + repoLower

	var ruleLink RepoLink
	if lk, ok := s.Links[fullLower]; ok {
		ruleLink = lk
	} else if lk, ok := s.Links[repoLower]; ok {
		ruleLink = lk
	}

	registryPage := computeRegistryPageURL(regLower, repoClean)

	labelSource := pickCleanLabelURL(labels, repoLower, "org.opencontainers.image.source", "org.label-schema.vcs-url")
	labelURL := pickCleanLabelURL(labels, repoLower, "org.opencontainers.image.url", "org.label-schema.url")
	labelDocs := pickCleanLabelURL(labels, repoLower, "org.opencontainers.image.documentation", "org.label-schema.usage")
	labelChangelog := pickCleanLabelURL(labels, repoLower, "org.opencontainers.image.changelog")

	var out RepoLink
	out.RegistryURL = registryPage

	// 1. 源码仓库（优先容器自身的 source/vcs-url，其次内置规则，再次 url 标签本身指向代码托管仓，或 ghcr/lscr 推导）
	if normSrc := normalizeSourceRepoURL(labelSource); normSrc != "" && !isRegistryImagePage(normSrc) {
		out.Repo = normSrc
	} else if ruleLink.Repo != "" && !isRegistryImagePage(ruleLink.Repo) {
		if isCodeHostRepoRoot(ruleLink.Repo) {
			out.Repo = normalizeSourceRepoURL(ruleLink.Repo)
		} else if out.Website == "" {
			out.Website = cleanHTTPURL(ruleLink.Repo)
		}
	}
	if out.Repo == "" && isCodeHostRepoRoot(labelURL) {
		out.Repo = normalizeSourceRepoURL(labelURL)
	}
	if out.Repo == "" {
		switch regLower {
		case "ghcr.io":
			if parts := strings.Split(repoClean, "/"); len(parts) >= 2 {
				out.Repo = "https://github.com/" + parts[0] + "/" + parts[1]
			}
		case "lscr.io":
			parts := strings.SplitN(repoClean, "/", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "linuxserver") {
				out.Repo = "https://github.com/linuxserver/docker-" + parts[1]
			}
		}
	}

	// 2. 官方网站（不因非源码仓而隐藏；仅与已有源码仓/镜像仓完全同址时去重）
	for _, cand := range []string{labelURL, ruleLink.Website} {
		u := cleanHTTPURL(cand)
		if u == "" || isRegistryImagePage(u) {
			continue
		}
		if !sameNormalizedURL(u, out.Repo) && !sameNormalizedURL(u, out.RegistryURL) {
			out.Website = u
			break
		}
	}

	// 3. 发行说明（显式 changelog/规则优先；否则若源码仓为代码托管仓则自动补齐 /releases）
	for _, cand := range []string{labelChangelog, ruleLink.Releases} {
		if u := cleanHTTPURL(cand); u != "" {
			out.Releases = u
			break
		}
	}
	if out.Releases == "" && out.Repo != "" {
		out.Releases = deriveReleasesFromRepo(out.Repo)
	}

	// 4. 官方文档（与官网/源码仓/发行说明/镜像仓不同址时独立保留）
	for _, cand := range []string{labelDocs, ruleLink.Docs} {
		u := cleanHTTPURL(cand)
		if u == "" || isRegistryImagePage(u) {
			continue
		}
		if !sameNormalizedURL(u, out.Repo) &&
			!sameNormalizedURL(u, out.Website) &&
			!sameNormalizedURL(u, out.Releases) &&
			!sameNormalizedURL(u, out.RegistryURL) {
			out.Docs = u
			break
		}
	}

	// 5. 兼容首页卡片底栏：若无独立源码仓，回退使用官网或镜像仓库页作为 Repo
	if out.Repo == "" {
		if ruleLink.Repo != "" {
			out.Repo = cleanHTTPURL(ruleLink.Repo)
		} else if out.RegistryURL != "" {
			out.Repo = out.RegistryURL
		} else if out.Website != "" {
			out.Repo = out.Website
		}
	}

	return out
}

func computeRegistryPageURL(regLower, repoClean string) string {
	if repoClean == "" {
		return ""
	}
	repoLower := strings.ToLower(repoClean)
	switch regLower {
	case "docker.io", "":
		if strings.HasPrefix(repoLower, "library/") {
			return "https://hub.docker.com/_/" + strings.TrimPrefix(repoLower, "library/")
		}
		return "https://hub.docker.com/r/" + repoClean
	case "quay.io":
		return "https://quay.io/repository/" + repoClean
	}
	return ""
}

func pickCleanLabelURL(labels map[string]string, repoLower string, keys ...string) string {
	if len(labels) == 0 {
		return ""
	}
	for _, k := range keys {
		raw := strings.TrimSpace(labels[k])
		if raw == "" {
			continue
		}
		u := cleanHTTPURL(raw)
		if u == "" || isInheritedBaseLabelURL(u, repoLower) {
			continue
		}
		return u
	}
	return ""
}

func cleanHTTPURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = strings.TrimPrefix(s, "git+")
	if strings.HasPrefix(s, "git://github.com/") {
		s = "https://" + strings.TrimPrefix(s, "git://")
	} else if strings.HasPrefix(s, "git@github.com:") {
		s = "https://github.com/" + strings.TrimPrefix(s, "git@github.com:")
	} else if strings.HasPrefix(s, "ssh://git@github.com/") {
		s = "https://github.com/" + strings.TrimPrefix(s, "ssh://git@github.com/")
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	if h == "localhost" || h == "127.0.0.1" || !strings.Contains(h, ".") {
		return ""
	}
	u.Fragment = ""
	return u.String()
}

func normalizeSourceRepoURL(raw string) string {
	c := cleanHTTPURL(raw)
	if c == "" {
		return ""
	}
	u, err := url.Parse(c)
	if err != nil {
		return ""
	}
	hostLower := strings.ToLower(u.Hostname())
	p := strings.Trim(u.Path, "/")
	p = strings.TrimSuffix(p, ".git")
	if p == "" {
		return ""
	}
	segs := strings.Split(p, "/")
	switch hostLower {
	case "github.com", "www.github.com", "gitee.com", "codeberg.org":
		if len(segs) < 2 || segs[0] == "orgs" || segs[0] == "users" || segs[0] == "marketplace" || segs[0] == "sponsors" {
			return c
		}
		h := strings.TrimPrefix(hostLower, "www.")
		return "https://" + h + "/" + segs[0] + "/" + strings.TrimSuffix(segs[1], ".git")
	case "gitlab.com", "www.gitlab.com":
		if idx := sliceIndex(segs, "-"); idx >= 2 {
			segs = segs[:idx]
		}
		if len(segs) >= 2 {
			return "https://gitlab.com/" + strings.Join(segs, "/")
		}
	}
	u.Path = "/" + p
	u.RawQuery = ""
	return u.String()
}

func isCodeHostRepoRoot(raw string) bool {
	c := cleanHTTPURL(raw)
	if c == "" {
		return false
	}
	u, err := url.Parse(c)
	if err != nil {
		return false
	}
	h := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	segs := strings.Split(strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/"), "/")
	switch h {
	case "github.com", "gitee.com", "codeberg.org":
		if len(segs) < 2 || segs[0] == "" || segs[1] == "" {
			return false
		}
		if segs[0] == "orgs" || segs[0] == "users" || segs[0] == "topics" || segs[0] == "sponsors" {
			return false
		}
		// 排除显式指向 Wiki / GitHub Pages 文档子路径的 URL，避免把文档站误归为源码根
		if len(segs) >= 3 && (segs[2] == "wiki" || segs[2] == "pkgs" || segs[2] == "discussions") {
			return false
		}
		return true
	case "gitlab.com":
		return len(segs) >= 2 && segs[0] != "" && segs[1] != ""
	}
	return false
}

func deriveReleasesFromRepo(repoURL string) string {
	u, err := url.Parse(repoURL)
	if err != nil {
		return ""
	}
	h := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	switch h {
	case "github.com", "gitee.com", "codeberg.org":
		if len(segs) == 2 && segs[0] != "" && segs[1] != "" {
			return "https://" + h + "/" + segs[0] + "/" + segs[1] + "/releases"
		}
	case "gitlab.com":
		if len(segs) >= 2 && segs[0] != "" {
			return "https://gitlab.com/" + strings.Join(segs, "/") + "/-/releases"
		}
	}
	return ""
}

func isRegistryImagePage(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "hub.docker.com" || h == "quay.io" || h == "ghcr.io" || h == "lscr.io"
}

func sameNormalizedURL(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	na := strings.TrimRight(strings.ToLower(strings.TrimSpace(a)), "/")
	nb := strings.TrimRight(strings.ToLower(strings.TrimSpace(b)), "/")
	na = strings.TrimPrefix(na, "https://www.")
	nb = strings.TrimPrefix(nb, "https://www.")
	na = strings.TrimPrefix(na, "http://www.")
	nb = strings.TrimPrefix(nb, "http://www.")
	na = strings.TrimPrefix(na, "https://")
	nb = strings.TrimPrefix(nb, "https://")
	na = strings.TrimPrefix(na, "http://")
	nb = strings.TrimPrefix(nb, "http://")
	return na == nb
}

// isInheritedBaseLabelURL 过滤由基础操作系统/语言运行时镜像（如 FROM alpine / ubuntu / debian / python / node）遗留给业务镜像的标签。
func isInheritedBaseLabelURL(rawURL, repoLower string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	hp := strings.ToLower(u.Hostname() + u.Path)

	type baseRule struct {
		urlSubstr string
		ownRepos  []string
	}
	rules := []baseRule{
		{"alpinelinux.org", []string{"library/alpine", "alpine"}},
		{"github.com/alpinelinux/", []string{"library/alpine", "alpine"}},
		{"debian.org", []string{"library/debian", "debian"}},
		{"github.com/debuerreotype/", []string{"library/debian", "debian"}},
		{"ubuntu.com", []string{"library/ubuntu", "ubuntu"}},
		{"launchpad.net/ubuntu", []string{"library/ubuntu", "ubuntu"}},
		{"launchpad.net/cloud-images", []string{"library/ubuntu", "ubuntu"}},
		{"centos.org", []string{"library/centos", "centos"}},
		{"rockylinux.org", []string{"library/rockylinux", "rockylinux"}},
		{"almalinux.org", []string{"library/almalinux", "almalinux"}},
		{"fedoraproject.org", []string{"library/fedora", "fedora"}},
		{"busybox.net", []string{"library/busybox", "busybox"}},
		{"python.org", []string{"library/python", "python"}},
		{"nodejs.org", []string{"library/node", "node"}},
		{"github.com/nodejs/docker-node", []string{"library/node", "node"}},
		{"golang.org", []string{"library/golang", "golang"}},
		{"go.dev", []string{"library/golang", "golang"}},
		{"adoptium.net", []string{"library/eclipse-temurin", "library/openjdk"}},
		{"github.com/adoptium/", []string{"library/eclipse-temurin", "library/openjdk"}},
		{"github.com/docker-library/", []string{"library/"}},
		{"hub.docker.com/_/", []string{"library/"}},
		{"buildpacks.io", []string{"buildpacks", "paketo"}},
		{"paketo.io", []string{"paketo"}},
	}
	for _, br := range rules {
		if !strings.Contains(hp, br.urlSubstr) {
			continue
		}
		matchedOwn := false
		for _, own := range br.ownRepos {
			if strings.HasSuffix(own, "/") {
				if strings.HasPrefix(repoLower, own) {
					matchedOwn = true
					break
				}
			} else if repoLower == own || strings.HasSuffix(repoLower, "/"+own) {
				matchedOwn = true
				break
			}
		}
		if !matchedOwn {
			return true
		}
	}
	return false
}

func sliceIndex(s []string, target string) int {
	for i, v := range s {
		if v == target {
			return i
		}
	}
	return -1
}
