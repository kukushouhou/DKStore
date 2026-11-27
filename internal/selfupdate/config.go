package selfupdate

import (
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/kukushouhou/DKStore/internal/version"
)

const (
	// OfficialMirrorHost 是 DKStore 官方专属 GitHub Releases 实时转发节点（仅白名单转发官方主仓库）。
	OfficialMirrorHost = "https://gh.h5.gg"
	// OfficialMirrorEnabled 控制是否在前端开放官方镜像源入口（待 gh.h5.gg Worker 正式上线后开启为 true）。
	OfficialMirrorEnabled = false

	PolicyAuto   = "auto"
	PolicyNotify = "notify"
	PolicyManual = "manual"

	ChannelGitHub = "github"
	ChannelMirror = "mirror"
	ChannelCustom = "custom"

	CustomModePrefix = "prefix"
	CustomModeHost   = "host"

	settingKeyConfig         = "self_update_config"
	settingKeyNotifiedVer    = "self_update_notified_ver"
	settingKeyUpgradedNotify = "self_update_upgraded_notify"
)

// PresetMirrors 是内置的常用 GitHub Release 加速节点候选列表。
var PresetMirrors = []string{
	"https://gh-proxy.org",
	"https://gh-proxy.com",
	"https://ghfast.top",
	"https://ghproxy.net",
}

var (
	ErrInvalidRepo       = errors.New("invalid repository format (expected owner/repo)")
	ErrCustomHostEmpty   = errors.New("mirror URL is required")
	ErrCustomHostInvalid = errors.New("invalid mirror URL")
	ErrMirrorRepoLocked  = errors.New("official mirror only supports the official repository")
)

var validRepoRe = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

// Config 保存 DKStore 版本自更新的策略、源码仓库与下载源配置。
type Config struct {
	Policy     string `json:"policy"`     // "auto" | "notify" | "manual"，默认 "auto"
	Repo       string `json:"repo"`       // 形如 "kukushouhou/DKStore"
	Channel    string `json:"channel"`    // "github" | "mirror" | "custom"
	CustomHost string `json:"customHost"` // 形如 "https://gh-proxy.org"
	CustomMode string `json:"customMode"` // "prefix" | "host"
}

// DefaultRepoSlug 返回编译期注入或默认的源码仓库路径。
func DefaultRepoSlug() string {
	r := strings.TrimSpace(version.DefaultRepo)
	if r == "" {
		return "kukushouhou/DKStore"
	}
	return r
}

// DefaultConfig 返回默认配置（默认自动更新 DKStore，直连 GitHub 官方仓库）。
func DefaultConfig() Config {
	return Config{
		Policy:     PolicyAuto,
		Repo:       DefaultRepoSlug(),
		Channel:    ChannelGitHub,
		CustomHost: "",
		CustomMode: CustomModePrefix,
	}
}

// ActiveOfficialMirrorHost 返回当前对外启用的官方镜像域名；未启用时返回空字符串（前端自动隐藏入口）。
func ActiveOfficialMirrorHost() string {
	if !OfficialMirrorEnabled {
		return ""
	}
	return OfficialMirrorHost
}

// IsOfficialRepo 判断给定仓库是否为官方主仓库。
func IsOfficialRepo(repo string) bool {
	return strings.EqualFold(strings.TrimSpace(repo), DefaultRepoSlug())
}

// NormalizeRepo 归一化用户输入或粘贴的仓库路径（兼容粘贴完整 github.com URL 的情况）。
func NormalizeRepo(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return DefaultRepoSlug(), nil
	}
	lower := strings.ToLower(s)
	for _, pfx := range []string{"https://github.com/", "http://github.com/", "github.com/"} {
		if strings.HasPrefix(lower, pfx) {
			s = s[len(pfx):]
			break
		}
	}
	s = strings.Trim(s, "/")
	if idx := strings.Index(s, "/releases"); idx > 0 {
		s = s[:idx]
	}
	s = strings.TrimSuffix(s, ".git")
	s = strings.Trim(s, "/")
	if strings.Contains(s, "..") || !validRepoRe.MatchString(s) {
		return "", ErrInvalidRepo
	}
	return s, nil
}

// NormalizeCustomHost 归一化自定义镜像域名（自动补齐 https:// 并去除末尾斜杠）。
func NormalizeCustomHost(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || strings.TrimSpace(u.Host) == "" {
		return "", ErrCustomHostInvalid
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", ErrCustomHostInvalid
	}
	clean := u.Scheme + "://" + u.Host + strings.TrimRight(u.Path, "/")
	return strings.TrimRight(clean, "/"), nil
}

// NormalizeAndValidate 校验并归一化自更新配置。requireComplete=true 时要求自定义镜像必填地址。
func NormalizeAndValidate(in Config, requireComplete bool) (Config, error) {
	pol := strings.ToLower(strings.TrimSpace(in.Policy))
	switch pol {
	case PolicyAuto, PolicyNotify, PolicyManual:
	default:
		pol = PolicyAuto
	}

	repo, err := NormalizeRepo(in.Repo)
	if err != nil {
		return Config{}, err
	}
	ch := strings.ToLower(strings.TrimSpace(in.Channel))
	switch ch {
	case ChannelGitHub, ChannelMirror, ChannelCustom:
	default:
		ch = ChannelGitHub
	}

	mode := strings.ToLower(strings.TrimSpace(in.CustomMode))
	if mode != CustomModeHost {
		mode = CustomModePrefix
	}

	host, err := NormalizeCustomHost(in.CustomHost)
	if err != nil {
		return Config{}, err
	}

	if ch == ChannelMirror {
		if !OfficialMirrorEnabled {
			ch = ChannelGitHub
		} else if !IsOfficialRepo(repo) {
			if requireComplete {
				return Config{}, ErrMirrorRepoLocked
			}
			ch = ChannelGitHub
		}
	}
	if ch == ChannelCustom && host == "" && requireComplete {
		return Config{}, ErrCustomHostEmpty
	}

	return Config{
		Policy:     pol,
		Repo:       repo,
		Channel:    ch,
		CustomHost: host,
		CustomMode: mode,
	}, nil
}

// EffectiveBaseURL 根据配置计算实际请求的仓库根地址（无末尾斜杠）。
func EffectiveBaseURL(cfg Config) string {
	repo := strings.TrimSpace(cfg.Repo)
	if repo == "" {
		repo = DefaultRepoSlug()
	}
	switch cfg.Channel {
	case ChannelMirror:
		if OfficialMirrorEnabled && OfficialMirrorHost != "" {
			return strings.TrimRight(OfficialMirrorHost, "/") + "/https://github.com/" + DefaultRepoSlug()
		}
		return "https://github.com/" + repo
	case ChannelCustom:
		h := strings.TrimRight(strings.TrimSpace(cfg.CustomHost), "/")
		if h == "" {
			return "https://github.com/" + repo
		}
		if cfg.CustomMode == CustomModeHost {
			return h + "/" + repo
		}
		return h + "/https://github.com/" + repo
	default:
		return "https://github.com/" + repo
	}
}
