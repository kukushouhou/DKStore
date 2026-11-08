// Package i18n 提供后端统一的中英双语（zh-CN / en-US）消息、风险警告、诊断步骤、API 错误与推送通知模板（实施文档 §10.4）。
package i18n

import (
	"fmt"
	"net/http"
	"strings"
)

// Lang 表示受支持的语言标识。
type Lang string

const (
	ZhCN Lang = "zh-CN"
	EnUS Lang = "en-US"
)

// Normalize 将任意语言标识（如 "zh"、"zh-CN"、"en"、"en-US,en;q=0.9"）归一化为 ZhCN 或 EnUS。
func Normalize(raw string) Lang {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ZhCN
	}
	if comma := strings.Index(s, ","); comma >= 0 {
		s = s[:comma]
	}
	if semi := strings.Index(s, ";"); semi >= 0 {
		s = s[:semi]
	}
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "en") {
		return EnUS
	}
	if strings.HasPrefix(s, "zh") {
		return ZhCN
	}
	return ZhCN
}

// FromRequest 从 HTTP 请求头（X-DKStore-Lang 或 Accept-Language）解析语言，缺省回退到 fallback。
func FromRequest(r *http.Request, fallback Lang) Lang {
	if r == nil {
		return fallback
	}
	if v := strings.TrimSpace(r.Header.Get("X-DKStore-Lang")); v != "" {
		return Normalize(v)
	}
	if v := strings.TrimSpace(r.Header.Get("Accept-Language")); v != "" {
		return Normalize(v)
	}
	if fallback != "" {
		return fallback
	}
	return ZhCN
}

// FormatReason 根据结构化原因码和参数生成对应语言的风险警告文案。
func FormatReason(lang Lang, reason, dbName, fromVer, toVer string) string {
	switch reason {
	case "major_bump":
		if lang == EnUS {
			return fmt.Sprintf("Major version upgrade (%s → %s) may contain breaking changes. Please review the official release notes before updating.", fromVer, toVer)
		}
		return fmt.Sprintf("跨大版本升级（%s → %s），可能包含破坏性变更，请先查阅官方更新说明。", fromVer, toVer)
	case "database":
		if lang == EnUS {
			return fmt.Sprintf("%s is a database service. Please read the official release notes and back up your data before updating.", dbName)
		}
		return fmt.Sprintf("%s 属于数据库服务，升级前请阅读官方发行说明并确认数据已备份。", dbName)
	case "database_suspected":
		if lang == EnUS {
			return fmt.Sprintf("Identified as a suspected %s database image; placed in Caution Updates under conservative database rules.", dbName)
		}
		return fmt.Sprintf("该镜像识别为疑似 %s 数据库，已按数据库保守规则归入谨慎更新。", dbName)
	case "database_cross_series":
		if lang == EnUS {
			return fmt.Sprintf("Target image (%s → %s) crosses %s release series! Please read the upgrade guide and back up your data first.", fromVer, toVer, dbName)
		}
		return fmt.Sprintf("目标镜像（%s → %s）已跨 %s 数据库发行系列，请务必先阅读升级说明并备份数据！", fromVer, toVer, dbName)
	case "pinned_digest":
		if lang == EnUS {
			return "This service pins a specific SHA256 digest. The remote tag has new content, but pinned digests will not update automatically."
		}
		return "该镜像已固定 SHA256 摘要；远端标签已有新内容，固定摘要不会自动跟随更新。"
	case "republished":
		if lang == EnUS {
			return "The image for the same version tag has been rebuilt and republished."
		}
		return "同版本标签镜像已重新构建发布。"
	default:
		return ""
	}
}

// FormatHint 根据结构化 hint 编码（"kind:version"）生成对应语言的附加提示文案。
func FormatHint(lang Lang, hint string) string {
	if hint == "" {
		return ""
	}
	kind, val, ok := strings.Cut(hint, ":")
	if !ok {
		return hint
	}
	switch kind {
	case "db_new_series":
		if lang == EnUS {
			return fmt.Sprintf("Newer release series %s is also available (cross-series database upgrades are not pushed as regular updates).", val)
		}
		return fmt.Sprintf("另有跨发行系列版本 %s（数据库不默认跨系列推送）", val)
	case "db_only_new_series":
		if lang == EnUS {
			return fmt.Sprintf("Newer release series %s exists (current series is up to date).", val)
		}
		return fmt.Sprintf("存在新发行系列 %s（当前系列已是最新）", val)
	case "in_series_update":
		if lang == EnUS {
			return fmt.Sprintf("Update within current major version is also available: %s", val)
		}
		return fmt.Sprintf("当前主版本内另有更新：%s", val)
	case "higher_tag":
		if lang == EnUS {
			return fmt.Sprintf("Higher version tag also available in registry: %s", val)
		}
		return fmt.Sprintf("仓库同时存在更高版本标签：%s", val)
	default:
		return hint
	}
}

// FormatTargetVersion 转换特殊的占位目标版本标识（如 "@new_digest"）。
func FormatTargetVersion(lang Lang, targetVer string) string {
	switch targetVer {
	case "", "@new_digest":
		if lang == EnUS {
			return "New image detected"
		}
		return "检测到新镜像"
	default:
		if base, ok := strings.CutSuffix(targetVer, "@republished"); ok {
			if lang == EnUS {
				return base + " (republished)"
			}
			return base + "（重新发布）"
		}
		return targetVer
	}
}

// FormatRegistryError 将 Registry 错误分类格式化为对应语言的说明。
func FormatRegistryError(lang Lang, errKind, rawMsg string) string {
	switch errKind {
	case "auth_required":
		if lang == EnUS {
			return "Registry authentication required or access denied (HTTP 401/403). Please configure registry credentials in Settings."
		}
		return "镜像仓库需要认证或无访问权限（HTTP 401/403），请在“设置 → 仓库凭证”中配置账号。"
	case "not_found":
		if lang == EnUS {
			return "Image or tag not found in remote registry (HTTP 404)."
		}
		return "远端仓库中未找到该镜像或标签（HTTP 404）。"
	case "rate_limited":
		if lang == EnUS {
			return "Registry rate limit exceeded (HTTP 429). Please retry later or configure a registry account."
		}
		return "镜像仓库请求触发限流（HTTP 429），请稍后重试或配置仓库账号。"
	case "network":
		if lang == EnUS {
			return "Network error while contacting registry: " + rawMsg
		}
		return "访问镜像仓库网络失败：" + rawMsg
	case "registry_error":
		if lang == EnUS {
			return "Registry returned an unexpected error: " + rawMsg
		}
		return "镜像仓库返回异常：" + rawMsg
	default:
		return rawMsg
	}
}

// APIErrorMessage 根据错误码返回对应语言的默认错误消息。
func APIErrorMessage(lang Lang, code, fallback string) string {
	if lang == EnUS {
		switch code {
		case "not_found":
			return "Endpoint or resource not found"
		case "unauthorized":
			return "Authentication required, please sign in"
		case "invalid_credentials":
			return "Incorrect password"
		case "bad_request":
			if fallback != "" {
				return fallback
			}
			return "Invalid request parameters"
		case "db_error":
			return "Database error"
		case "check_failed":
			return "Update check failed"
		}
	} else {
		switch code {
		case "not_found":
			return "接口或资源不存在"
		case "unauthorized":
			return "请先登录管理员账号"
		case "invalid_credentials":
			return "密码不正确"
		case "bad_request":
			if fallback != "" {
				return fallback
			}
			return "请求参数有误"
		case "db_error":
			return "数据库操作失败"
		case "check_failed":
			return "检查更新失败"
		}
	}
	return fallback
}

// NotifyItem 是用于生成推送通知消息的单条更新摘要。
type NotifyItem struct {
	HostName       string
	AppName        string
	ServiceName    string
	Category       string // normal | caution
	CurrentVersion string
	TargetVersion  string
	Reason         string
	DatabaseName   string
}

// FormatNotification 生成中英双语的更新提醒推送标题与正文（实施文档 §8.2 / §10.4）。
func FormatNotification(lang Lang, items []NotifyItem) (title, body string) {
	var normal, caution []NotifyItem
	for _, it := range items {
		if it.Category == "caution" {
			caution = append(caution, it)
		} else {
			normal = append(normal, it)
		}
	}

	if lang == EnUS {
		title = fmt.Sprintf("DKStore: %d container update(s) available", len(items))
		var sb strings.Builder
		if len(normal) > 0 {
			sb.WriteString(fmt.Sprintf("[Available Updates (%d)]\n", len(normal)))
			for _, it := range normal {
				cur := it.CurrentVersion
				if cur == "" {
					cur = "current"
				}
				tgt := FormatTargetVersion(EnUS, it.TargetVersion)
				sb.WriteString(fmt.Sprintf("• %s (%s): %s → %s\n", formatAppSvc(it), it.HostName, cur, tgt))
			}
		}
		if len(caution) > 0 {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(fmt.Sprintf("[Caution Updates (%d)]\n", len(caution)))
			for _, it := range caution {
				cur := it.CurrentVersion
				if cur == "" {
					cur = "current"
				}
				tgt := FormatTargetVersion(EnUS, it.TargetVersion)
				warn := FormatReason(EnUS, it.Reason, it.DatabaseName, cur, tgt)
				sb.WriteString(fmt.Sprintf("⚠ %s (%s): %s → %s\n  %s\n", formatAppSvc(it), it.HostName, cur, tgt, warn))
			}
		}
		return title, strings.TrimSpace(sb.String())
	}

	title = fmt.Sprintf("DKStore：发现 %d 个应用有新版本", len(items))
	var sb strings.Builder
	if len(normal) > 0 {
		sb.WriteString(fmt.Sprintf("【可更新（%d）】\n", len(normal)))
		for _, it := range normal {
			cur := it.CurrentVersion
			if cur == "" {
				cur = "当前版本"
			}
			tgt := FormatTargetVersion(ZhCN, it.TargetVersion)
			sb.WriteString(fmt.Sprintf("• %s（%s）：%s → %s\n", formatAppSvc(it), it.HostName, cur, tgt))
		}
	}
	if len(caution) > 0 {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("【需谨慎更新（%d）】\n", len(caution)))
		for _, it := range caution {
			cur := it.CurrentVersion
			if cur == "" {
				cur = "当前版本"
			}
			tgt := FormatTargetVersion(ZhCN, it.TargetVersion)
			warn := FormatReason(ZhCN, it.Reason, it.DatabaseName, cur, tgt)
			sb.WriteString(fmt.Sprintf("⚠ %s（%s）：%s → %s\n  %s\n", formatAppSvc(it), it.HostName, cur, tgt, warn))
		}
	}
	return title, strings.TrimSpace(sb.String())
}

// FormatTestNotification 生成测试推送的双语标题和正文。
func FormatTestNotification(lang Lang) (title, body string) {
	if lang == EnUS {
		return "DKStore Test Notification", "Notification channel is configured and working properly."
	}
	return "DKStore 测试通知", "消息推送渠道配置成功，连接正常。"
}

func formatAppSvc(it NotifyItem) string {
	if it.ServiceName == "" || it.ServiceName == "main" || it.ServiceName == it.AppName {
		return it.AppName
	}
	return it.AppName + "/" + it.ServiceName
}
