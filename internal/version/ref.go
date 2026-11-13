package version

import (
	"fmt"
	"strings"
)

// ParsedRef 是规范化后的镜像引用（实施文档 §5.1）。
type ParsedRef struct {
	Raw        string `json:"raw"`
	Registry   string `json:"registry"`   // docker.io / ghcr.io / lscr.io / 10.0.0.1:5000
	Repository string `json:"repository"` // library/nginx / home-assistant/home-assistant
	Tag        string `json:"tag"`        // 缺省为 latest（当 Digest 为空时）
	Digest     string `json:"digest"`     // 引用中显式固定的 @sha256:...
}

// IsPinnedDigest 表示引用是否显式固定了 @sha256:... 摘要。
func (p ParsedRef) IsPinnedDigest() bool {
	return p.Digest != ""
}

// ParseImageRef 解析并规范化镜像引用字符串。
func ParseImageRef(raw string) (ParsedRef, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ParsedRef{}, fmt.Errorf("镜像引用为空")
	}
	res := ParsedRef{Raw: s}

	// 1. 拆分 @sha256:...
	if at := strings.Index(s, "@"); at >= 0 {
		res.Digest = s[at+1:]
		s = s[:at]
	}

	// 2. 拆分第一段判断是否包含 registry 域名
	var reg, rest string
	firstSlash := strings.Index(s, "/")
	if firstSlash < 0 {
		reg = "docker.io"
		rest = "library/" + s
	} else {
		first := s[:firstSlash]
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			reg = normalizeRegistry(first)
			rest = s[firstSlash+1:]
		} else {
			reg = "docker.io"
			rest = s
		}
	}

	// 3. 在 rest 的最后一段拆分 :tag
	lastSlash := strings.LastIndex(rest, "/")
	colon := strings.LastIndex(rest, ":")
	if colon > lastSlash {
		res.Tag = rest[colon+1:]
		rest = rest[:colon]
	} else if res.Digest == "" {
		res.Tag = "latest"
	}

	if reg == "docker.io" && !strings.Contains(rest, "/") {
		rest = "library/" + rest
	}
	if rest == "" {
		return ParsedRef{}, fmt.Errorf("镜像仓库名为空: %s", raw)
	}
	res.Registry = reg
	res.Repository = rest
	return res, nil
}

// WithTag 生成同一仓库下替换为新标签的引用字符串（保持用户的简写习惯）。
func (p ParsedRef) WithTag(newTag string) string {
	repo := p.Repository
	if p.Registry == "docker.io" {
		repo = strings.TrimPrefix(repo, "library/")
		return repo + ":" + newTag
	}
	return p.Registry + "/" + repo + ":" + newTag
}

// NormalizeRegistry 规范化仓库域名（去除可选协议头与尾部路径，并归一 Docker Hub / GHCR 常见镜像域名）。
func NormalizeRegistry(r string) string {
	s := strings.ToLower(strings.TrimSpace(r))
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	if idx := strings.IndexByte(s, '/'); idx >= 0 {
		s = s[:idx]
	}
	switch s {
	case "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com", "docker.io",
		"docker.1ms.run", "docker.3ms.run", "docker.m.daocloud.io", "dockerproxy.com",
		"docker.nju.edu.cn", "hub-mirror.c.163.com", "mirror.baidubce.com":
		return "docker.io"
	case "ghcr.io", "ghcr.nju.edu.cn", "ghcr.m.daocloud.io", "ghcr-proxy.com":
		return "ghcr.io"
	default:
		return s
	}
}

func normalizeRegistry(r string) string {
	return NormalizeRegistry(r)
}
