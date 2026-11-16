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
	return FormatReasonWithRole(lang, reason, dbName, "", fromVer, toVer)
}

// FormatReasonWithRole 根据结构化原因码、组件名称与角色类型（database/mq/middleware）生成对应语言的风险警告文案。
func FormatReasonWithRole(lang Lang, reason, dbName, roleKind, fromVer, toVer string) string {
	switch reason {
	case "major_bump":
		if lang == EnUS {
			return fmt.Sprintf("Major version upgrade (%s → %s). Please check the release notes before updating.", fromVer, toVer)
		}
		return fmt.Sprintf("跨大版本升级（%s → %s），建议先查看新版更新说明。", fromVer, toVer)
	case "database", "database_suspected":
		switch roleKind {
		case "mq":
			if lang == EnUS {
				return fmt.Sprintf("This is a %s message queue service. Please verify message consumers and back up before updating.", dbName)
			}
			return fmt.Sprintf("这是 %s 消息队列服务，升级前建议确认消费状态并备份好数据。", dbName)
		case "middleware":
			if lang == EnUS {
				return fmt.Sprintf("This is a %s stateful middleware service. Please back up your data before updating.", dbName)
			}
			return fmt.Sprintf("这是 %s 有状态中间件服务，升级前建议先备份好数据。", dbName)
		default:
			if lang == EnUS {
				return fmt.Sprintf("This is a %s database service. Please back up your data before updating.", dbName)
			}
			return fmt.Sprintf("这是 %s 数据库服务，升级前建议先备份好数据。", dbName)
		}
	case "database_cross_series":
		switch roleKind {
		case "mq":
			if lang == EnUS {
				return fmt.Sprintf("%s message queue cross-series upgrade (%s → %s). Please read the upgrade guide first!", dbName, fromVer, toVer)
			}
			return fmt.Sprintf("%s 消息队列跨系列升级（%s → %s），请先查阅升级说明！", dbName, fromVer, toVer)
		case "middleware":
			if lang == EnUS {
				return fmt.Sprintf("%s middleware cross-series upgrade (%s → %s). Please back up your data and read the upgrade guide first!", dbName, fromVer, toVer)
			}
			return fmt.Sprintf("%s 中间件跨系列升级（%s → %s），请先备份数据并查阅升级说明！", dbName, fromVer, toVer)
		default:
			if lang == EnUS {
				return fmt.Sprintf("%s database major upgrade (%s → %s). Please back up your data and read the upgrade guide first!", dbName, fromVer, toVer)
			}
			return fmt.Sprintf("%s 数据库跨大版本升级（%s → %s），请先备份数据并查阅升级说明！", dbName, fromVer, toVer)
		}
	case "pinned_digest":
		if lang == EnUS {
			return "This container is pinned to a fixed version and will not update automatically."
		}
		return "当前容器锁定了固定版本，不会自动跟随更新。"
	case "republished":
		if lang == EnUS {
			return "The developer republished the package for this version."
		}
		return "开发者重新发布了该版本的安装包。"
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
	if strings.HasPrefix(rawMsg, detailPrefix) {
		return LocalizeDetail(lang, rawMsg)
	}
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
		case "setup_required":
			return "Please set an administrator password for DKStore first"
		case "password_already_set":
			return "Administrator password is already set; please sign in or change it in Settings"
		case "password_too_short":
			return "Password must be at least 6 characters"
		case "sse_unsupported":
			return "Server does not support event streaming (SSE)"
		case "updater_unavailable":
			return "Update module is not available"
		case "keygen_failed":
			if fallback != "" {
				return "Failed to generate SSH key: " + fallback
			}
			return "Failed to generate SSH key"
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
		case "setup_required":
			return "请先设置 DKStore 管理员密码"
		case "password_already_set":
			return "已设置管理员密码，请直接登录或在设置页修改密码"
		case "password_too_short":
			return "密码长度至少 6 位"
		case "sse_unsupported":
			return "服务端不支持事件推送（SSE）"
		case "updater_unavailable":
			return "更新模块不可用"
		case "keygen_failed":
			if fallback != "" {
				return "生成 SSH 密钥失败：" + fallback
			}
			return "生成 SSH 密钥失败"
		}
	}
	return fallback
}

// HostAuthHint 将宿主登录态核验失败原因码（internal/hostauth.Reason*）转为本地化说明；
// 未知原因码返回空字符串。
func HostAuthHint(lang Lang, code string) string {
	if lang == EnUS {
		switch code {
		case "no_host_cookie":
			return "No device login was detected. Sign in to the device admin page (DSM / LuCI) in this browser using the same address, or set a DKStore password below."
		case "invalid_host_cookie":
			return "The device login cookie has an unexpected format and cannot be verified."
		case "host_auth_unavailable":
			return "This device does not provide a usable login verification interface (e.g. LuCI is not installed or the system is customized)."
		case "host_session_invalid":
			return "The device login session is invalid or has expired. Sign in to the device admin page again and refresh."
		case "host_not_admin":
			return "The account signed in to the device is not an administrator."
		case "no_fnos_gateway":
			return "Please open DKStore from the fnOS desktop icon first, or sign in via the fnOS web desktop."
		}
		return ""
	}
	switch code {
	case "no_host_cookie":
		return "未检测到设备登录状态。请在本浏览器中用相同地址登录设备管理后台（DSM / LuCI），或在下方设置 DKStore 密码。"
	case "invalid_host_cookie":
		return "设备登录 Cookie 格式异常，无法核验。"
	case "host_auth_unavailable":
		return "本设备未提供可用的登录核验接口（例如未安装 LuCI 或为厂商定制系统）。"
	case "host_session_invalid":
		return "设备登录状态无效或已过期，请重新登录设备管理后台后刷新本页。"
	case "host_not_admin":
		return "当前登录设备的账号不是管理员。"
	case "no_fnos_gateway":
		return "请从飞牛 fnOS 桌面点击 DKStore 图标免密进入（在设置中配置密码后，即可通过端口直连访问）。"
	}
	return ""
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

// FormatUpdateError 根据更新错误编码生成面向普通用户的可读错误说明（立项文档 §7.2）。
func FormatUpdateError(lang Lang, code, detail string) string {
	detail = strings.TrimSpace(LocalizeDetail(lang, detail))
	if code == "pull_failed" && strings.Contains(detail, "ApplyLayer") && strings.Contains(detail, "archive/tar: invalid tar header") {
		return EngineMessage(lang, MsgDockerPullOldZstd)
	}
	if msg, ok := humanizeUpdateError(lang, code, detail); ok {
		return msg
	}
	if lang == EnUS {
		switch code {
		case "external_conflict":
			return "External change conflict detected: container or Compose YAML was modified outside DKStore. Please rescan before updating."
		case "vendor_managed_readonly":
			return "This container is managed by the platform's native Package Center and cannot be directly recreated by DKStore."
		case "capability_unsupported":
			return "The target platform does not support safe single-service updates without affecting other services."
		case "dsm_tag_change_unsupported":
			return "The DSM admin connection cannot switch versions for this container directly; please switch to an SSH connection to update it."
		case "dsm_cli_compose_unsupported":
			return "This container was installed via terminal commands. The DSM admin connection cannot modify its command-line config file; please switch to an SSH connection to update it."
		case "interrupted":
			if detail != "" {
				return detail
			}
			return "Task was interrupted by a DKStore restart; please verify the container state."
		case "connect_failed":
			if detail != "" {
				return "Cannot connect to target device: " + detail
			}
			return "Cannot connect to target device; please check if the device is online and credentials are valid."
		case "pull_failed":
			if detail == "" {
				return "Failed to download new image"
			}
			return "Failed to download new image: " + detail
		case "recreate_failed":
			if detail == "" {
				return "Failed to recreate and start new container"
			}
			return "Failed to recreate and start new container: " + detail
		case "verify_failed":
			if detail == "" {
				return "New container failed post-start check"
			}
			return "New container failed post-start check: " + detail
		default:
			if detail != "" {
				return detail
			}
			return "Update failed"
		}
	}
	switch code {
	case "external_conflict":
		return "检测到外部变更冲突：目标容器或 Compose 配置文件已被其他工具修改，为避免覆盖最新配置已中止操作，请先重新扫描。"
	case "vendor_managed_readonly":
		return "该容器由系统套件中心托管，不支持通过更新中心直接重建，请前往宿主套件中心升级。"
	case "capability_unsupported":
		return "目标平台当前不支持安全地仅更新单个服务，已阻止整项目重建以避免误升级数据库。"
	case "dsm_tag_change_unsupported":
		return "当前管理后台连接不支持直接为该容器切换版本，请改用 SSH 连接更新。"
	case "dsm_cli_compose_unsupported":
		return "该容器是在终端里用命令安装的，管理后台连接无法修改其命令行配置文件，请改用 SSH 连接更新。"
	case "interrupted":
		if detail != "" {
			return detail
		}
		return "DKStore 重启导致任务中断，请先确认容器运行状态。"
	case "connect_failed":
		if detail != "" {
			return "无法连接目标设备：" + detail
		}
		return "无法连接目标设备：请确认设备在线且连接凭据有效。"
	case "pull_failed":
		if detail == "" {
			return "下载新镜像失败"
		}
		return "下载新镜像失败：" + detail
	case "recreate_failed":
		if detail == "" {
			return "替换并启动新容器失败"
		}
		return "替换并启动新容器失败：" + detail
	case "verify_failed":
		if detail == "" {
			return "新容器启动状态异常"
		}
		return "新容器启动状态异常：" + detail
	default:
		if detail != "" {
			return detail
		}
		return "更新执行失败"
	}
}

func isRawEOFDetail(lower string) bool {
	s := strings.TrimSpace(lower)
	if s == "eof" || strings.Contains(s, "unexpected eof") || strings.Contains(s, "early eof") {
		return true
	}
	for _, tok := range []string{": eof", "— eof", "- eof", "(eof)", "\"eof\""} {
		if strings.Contains(s, tok) {
			return true
		}
	}
	return false
}

// HumanizeRawNetError 将 Go / 系统底层的英文网络与传输错误转为通俗可读的原因短语。
func HumanizeRawNetError(lang Lang, raw string) string {
	s := strings.TrimSpace(raw)
	lower := strings.ToLower(s)
	pick := func(zh, en string) string {
		if lang == EnUS {
			return en
		}
		return zh
	}
	switch {
	case isRawEOFDetail(lower) ||
		strings.Contains(lower, "connection reset by peer") ||
		strings.Contains(lower, "wsarecv") ||
		strings.Contains(lower, "broken pipe") ||
		strings.Contains(lower, "server sent goaway") ||
		strings.Contains(lower, "stream error"):
		return pick("网络连接中途断开", "network connection dropped mid-transfer")
	case strings.Contains(lower, "i/o timeout") ||
		strings.Contains(lower, "tls handshake timeout") ||
		strings.Contains(lower, "client.timeout") ||
		strings.Contains(lower, "context deadline exceeded"):
		return pick("连接超时", "connection timed out")
	case strings.Contains(lower, "no such host") ||
		strings.Contains(lower, "temporary failure in name resolution") ||
		strings.Contains(lower, "server misbehaving"):
		return pick("域名解析失败，请检查 DNS 设置", "DNS lookup failed; check DNS settings")
	case strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "network is unreachable") ||
		strings.Contains(lower, "no route to host"):
		return pick("目标地址无法连接，请检查网络或代理设置", "target address unreachable; check network or proxy")
	case strings.Contains(lower, "x509:") ||
		strings.Contains(lower, "certificate"):
		return pick("安全证书校验失败，请检查系统时间或代理配置", "TLS certificate check failed; check system time or proxy")
	default:
		return s
	}
}

func humanizeUpdateError(lang Lang, code, detail string) (string, bool) {
	if detail == "" {
		return "", false
	}
	lower := strings.ToLower(detail)
	pick := func(zh, en string) (string, bool) {
		if lang == EnUS {
			return en, true
		}
		return zh, true
	}

	// 已本地化的旧版 Docker zstd 提示直接原样返回，避免重复套前缀
	if strings.Contains(lower, "zstd") && (strings.Contains(detail, "20.10.10") || strings.Contains(lower, "docker")) {
		return detail, true
	}

	// 1. 磁盘与文件系统故障（全阶段通用）
	if strings.Contains(lower, "no space left on device") ||
		strings.Contains(lower, "disk quota exceeded") ||
		strings.Contains(lower, "insufficient space") {
		return pick(
			"设备存储空间不足：磁盘已满，请先清理无用镜像或文件",
			"Insufficient disk space on target device: disk is full; please clean up unused images or files",
		)
	}
	if strings.Contains(lower, "read-only file system") {
		return pick(
			"设备存储异常：存储目录处于只读状态，请检查磁盘健康状况或目录权限",
			"Target storage is read-only; please check disk health or directory permissions",
		)
	}

	// 2. 容器重建与启动冲突
	if strings.Contains(lower, "port is already allocated") ||
		strings.Contains(lower, "address already in use") {
		return pick(
			"新容器启动失败：所需端口已被其他程序占用，请排查端口冲突",
			"Failed to start new container: required port is already in use by another process",
		)
	}
	if strings.Contains(lower, "no matching manifest for") ||
		strings.Contains(lower, "exec format error") {
		return pick(
			"无法安装该版本：此镜像不支持当前设备的芯片架构（如 ARM / x86 不匹配）",
			"Cannot install this version: image does not support this device's CPU architecture",
		)
	}

	// 3. 镜像仓库权限、限流与版本不存在（主要发生在 pull_failed 或 compose pull 阶段）
	if code == "pull_failed" || strings.Contains(lower, "compose pull:") || strings.Contains(lower, "error pulling image") {
		if strings.Contains(lower, "toomanyrequests") ||
			strings.Contains(lower, "rate limit") ||
			strings.Contains(lower, "429 too many requests") ||
			strings.Contains(lower, "http 429") {
			return pick(
				"下载受限：镜像仓库请求过于频繁被临时限流，可配置仓库账号或镜像加速",
				"Download rate-limited: registry request limit reached; configure registry credentials or a mirror",
			)
		}
		if strings.Contains(lower, "repository does not exist") {
			return pick(
				"无法下载镜像：仓库中不存在该镜像，或私有仓库尚未配置登录凭据",
				"Cannot download image: repository does not exist or requires registry credentials",
			)
		}
		if strings.Contains(lower, "unauthorized") ||
			strings.Contains(lower, "authentication required") ||
			strings.Contains(lower, "pull access denied") ||
			strings.Contains(lower, "requested access to the resource is denied") ||
			strings.Contains(lower, "insufficient_scope") ||
			strings.Contains(lower, "http 401") ||
			strings.Contains(lower, "http 403") ||
			strings.Contains(lower, "status code 401") ||
			strings.Contains(lower, "status code 403") {
			return pick(
				"无法下载镜像：该仓库需要登录或当前账号无访问权限，请检查“仓库凭据”",
				"Cannot download image: registry authentication required or access denied; check Registry Credentials",
			)
		}
		if isRawEOFDetail(lower) ||
			strings.Contains(lower, "connection reset by peer") ||
			strings.Contains(lower, "broken pipe") ||
			strings.Contains(lower, "server sent goaway") ||
			strings.Contains(lower, "stream error") ||
			strings.Contains(lower, "tls: bad record mac") ||
			strings.Contains(lower, "bad gateway") ||
			strings.Contains(lower, "service unavailable") ||
			strings.Contains(lower, "http 502") ||
			strings.Contains(lower, "http 503") ||
			strings.Contains(lower, "status code 502") ||
			strings.Contains(lower, "status code 503") {
			return pick(
				"下载新镜像中断：网络连接中途断开，已下载的部分会保留",
				"Image download interrupted: network connection dropped mid-transfer; downloaded layers are preserved",
			)
		}
		if strings.Contains(lower, "i/o timeout") ||
			strings.Contains(lower, "tls handshake timeout") ||
			strings.Contains(lower, "client.timeout") ||
			strings.Contains(lower, "context deadline exceeded") ||
			strings.Contains(lower, "gateway timeout") ||
			strings.Contains(lower, "http 504") ||
			strings.Contains(lower, "status code 504") {
			return pick(
				"下载新镜像超时：连接镜像仓库过慢，请检查网络或镜像加速设置",
				"Image download timed out: registry connection is too slow; check network or mirror settings",
			)
		}
		if strings.Contains(lower, "manifest unknown") ||
			strings.Contains(lower, "tag not found") ||
			strings.Contains(lower, "not found: manifest") ||
			strings.Contains(lower, "http 404") ||
			strings.Contains(lower, "status code 404") {
			return pick(
				"找不到该版本镜像：可能已被作者移除，或镜像加速源尚未同步",
				"Target image version not found: it may have been removed or not yet synced to your mirror",
			)
		}
		if strings.Contains(lower, "x509:") ||
			strings.Contains(lower, "certificate") {
			return pick(
				"安全连接校验失败：请检查设备系统时间是否准确，或检查代理证书配置",
				"TLS certificate check failed: please verify device system time or proxy certificate settings",
			)
		}
		if strings.Contains(lower, "no such host") ||
			strings.Contains(lower, "temporary failure in name resolution") ||
			strings.Contains(lower, "server misbehaving") ||
			strings.Contains(lower, "connection refused") ||
			strings.Contains(lower, "network is unreachable") ||
			strings.Contains(lower, "no route to host") {
			return pick(
				"连不上镜像仓库：请检查设备的网络连接、DNS 或代理设置",
				"Cannot reach image registry: please check device network, DNS, or proxy settings",
			)
		}
	}

	// 4. 非拉取阶段的网络断连 / 超时兜底（如 SSH 中途断线）
	if isRawEOFDetail(lower) ||
		strings.Contains(lower, "connection reset by peer") ||
		strings.Contains(lower, "broken pipe") {
		return pick(
			"与目标设备的连接中途断开：请确认设备在线且网络稳定",
			"Connection to target device dropped mid-operation; please check device connectivity",
		)
	}
	if strings.Contains(lower, "i/o timeout") ||
		strings.Contains(lower, "context deadline exceeded") {
		return pick(
			"更新操作超时：目标设备响应过慢或网络连接不稳定",
			"Update operation timed out: target device responded too slowly or network was unstable",
		)
	}

	return "", false
}

// FormatRollbackReason 根据回退不可用原因码生成对应语言的说明（立项文档 §8.2）。
func FormatRollbackReason(lang Lang, code string) string {
	if lang == EnUS {
		switch code {
		case "no_exact_version_tag":
			return "Used a floating version (e.g. latest) before updating and the old local package was cleaned up; cannot roll back."
		case "tag_overwritten":
			return "The old version package has been cleaned up; cannot roll back."
		case "target_unreachable":
			return "Unable to verify historical tag in the remote registry (offline or authentication required)."
		case "compose_unwritable":
			return "Original Compose configuration file is missing or unreadable; cannot safely roll back."
		case "vendor_managed":
			return "Managed by platform Package Center; rollback is not supported."
		default:
			return ""
		}
	}
	switch code {
	case "no_exact_version_tag":
		return "升级前使用的是浮动版本（如 latest）且本机旧安装包已清理，无法回退。"
	case "tag_overwritten":
		return "旧版本安装包已被清理，无法回退。"
	case "target_unreachable":
		return "无法连接远端仓库验证历史标签有效性，暂不可回退。"
	case "compose_unwritable":
		return "未找到或无法读写原始 Compose 配置文件，暂不可回退。"
	case "vendor_managed":
		return "由系统套件中心管理，不支持回退。"
	default:
		return ""
	}
}
