// Package rules 嵌入并提供数据库识别、标签规则与常见仓库链接映射。
package rules

import (
	"embed"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed *.yaml
var rulesFS embed.FS

// DatabaseRule 定义一种数据库的识别与系列判断规则。
type DatabaseRule struct {
	Name    string   `yaml:"name"`
	Images  []string `yaml:"images"`
	Pattern string   `yaml:"pattern"`
	Series  string   `yaml:"series"` // "major" | "minor"
	Docs    string   `yaml:"docs"`
	re      *regexp.Regexp
}

// RepoLink 定义镜像对应的源码仓库与更新说明链接。
type RepoLink struct {
	Repo     string `yaml:"repo" json:"repo,omitempty"`
	Releases string `yaml:"releases" json:"releases,omitempty"`
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
func (s *Set) ResolveLinks(registry, repository, targetVersion string) RepoLink {
	repoLower := strings.ToLower(repository)
	fullLower := strings.ToLower(registry + "/" + repository)
	if link, ok := s.Links[fullLower]; ok {
		return enrichReleaseTag(link, targetVersion)
	}
	if link, ok := s.Links[repoLower]; ok {
		return enrichReleaseTag(link, targetVersion)
	}
	switch strings.ToLower(registry) {
	case "ghcr.io":
		gh := "https://github.com/" + repository
		return enrichReleaseTag(RepoLink{Repo: gh, Releases: gh + "/releases"}, targetVersion)
	case "lscr.io":
		parts := strings.SplitN(repository, "/", 2)
		if len(parts) == 2 && parts[0] == "linuxserver" {
			gh := "https://github.com/linuxserver/docker-" + parts[1]
			return RepoLink{Repo: gh, Releases: gh + "/releases"}
		}
	case "quay.io":
		return RepoLink{Repo: "https://quay.io/repository/" + repository}
	case "docker.io", "":
		if strings.HasPrefix(repoLower, "library/") {
			name := strings.TrimPrefix(repoLower, "library/")
			return RepoLink{Repo: "https://hub.docker.com/_/" + name}
		}
		return RepoLink{Repo: "https://hub.docker.com/r/" + repository}
	}
	return RepoLink{}
}

func enrichReleaseTag(link RepoLink, ver string) RepoLink {
	return link
}
