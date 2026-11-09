package i18n

import (
	"errors"
	"fmt"
)

// 本文件提供主机管理（internal/host）与群晖适配器（internal/platform/synology）的中英双语文案。

type hostMsg struct{ zh, en string }

var hostMessages = map[string]hostMsg{
	// 主机注册与保存
	"host.not_found":         {"设备不存在：%s", "Device not found: %s"},
	"host.name_required":     {"请填写设备名称", "Please enter a device name"},
	"host.ssh_required":      {"SSH 设备需填写主机地址与用户名", "SSH devices require a host address and user name"},
	"host.dsm_required":      {"群晖 DSM 网页接口模式需填写 DSM 地址、管理员账号与密码", "Synology DSM WebAPI mode requires the DSM URL, admin account and password"},
	"host.transport_invalid": {"不支持的连接方式：%s", "Unsupported connection type: %s"},
	"host.verify_failed":     {"连接校验未通过，请检查地址与凭据", "Connection verification failed, please check the address and credentials"},
	"host.secret_load":       {"读取已保存的凭据失败", "Failed to load saved credentials"},
	"host.save_failed":       {"保存设备失败", "Failed to save device"},

	// SSH 指纹
	"host.fp":         {"主机指纹 %s", "Host fingerprint %s"},
	"host.fp_changed": {"指纹已变化（可能重装过系统），已自动更新：%s → %s", "Fingerprint changed (system may have been reinstalled), updated automatically: %s → %s"},

	// 群晖 DSM WebAPI
	"dsm.not_configured":      {"未配置群晖 DSM WebAPI 连接信息", "Synology DSM WebAPI connection is not configured"},
	"dsm.missing_url_user":    {"未填写群晖 DSM 地址或管理员账号", "Synology DSM URL or admin account is missing"},
	"dsm.connect_failed":      {"连接群晖 DSM WebAPI（%s）失败", "Failed to connect to Synology DSM WebAPI (%s)"},
	"dsm.bad_response":        {"解析群晖 DSM %s 响应失败", "Failed to parse Synology DSM %s response"},
	"dsm.need_otp":            {"该群晖账号开启了双重验证（2FA），请输入 6 位动态验证码（验证一次后将自动保存设备令牌，后续无需重复输入）", "This Synology account has 2-factor authentication enabled. Please enter the 6-digit OTP code (a device token will be saved so you won't be asked again)"},
	"dsm.otp_invalid":         {"群晖双重验证（2FA）动态验证码无效或已过期，请重新输入", "The Synology 2FA OTP code is invalid or expired, please enter a new one"},
	"dsm.login_failed":        {"群晖 DSM 登录失败（错误码 %d），请检查账号密码及管理员权限", "Synology DSM login failed (error code %d). Please check the account, password and admin permission"},
	"dsm.api_failed":          {"调用群晖接口 %s.%s 失败（错误码 %d）", "Synology API %s.%s failed (error code %d)"},
	"dsm.no_container_mgr":    {"无法访问群晖 Container Manager 项目接口（请确认已安装 Container Manager 且账号具备管理员权限）", "Cannot access the Synology Container Manager project API (make sure Container Manager is installed and the account is an administrator)"},
	"dsm.task_failed":         {"群晖任务 %s 执行失败：%s", "Synology task %s failed: %s"},
	"dsm.build_failed":        {"群晖 Project 重建失败（build_stream 与 build 均失败）", "Synology Project rebuild failed (both build_stream and build failed)"},
	"dsm.project_update_fail": {"写入群晖 Project 配置失败", "Failed to write Synology Project configuration"},
	"dsm.synowebapi_failed":   {"synowebapi 返回失败", "synowebapi returned failure"},
}

// HostText 返回主机 / 群晖相关文案的指定语言版本；args 按 fmt 规则格式化。
func HostText(lang Lang, key string, args ...any) string {
	m, ok := hostMessages[key]
	if !ok {
		return key
	}
	tpl := m.zh
	if lang == EnUS {
		tpl = m.en
	}
	if len(args) == 0 {
		return tpl
	}
	return fmt.Sprintf(tpl, args...)
}

// Localizer 由可按语言渲染文案的错误类型实现。
type Localizer interface {
	Localize(lang Lang) string
}

// LocalizeErr 若错误链中存在 Localizer，则按语言渲染；否则返回 err.Error()。
func LocalizeErr(lang Lang, err error) string {
	if err == nil {
		return ""
	}
	var l Localizer
	if errors.As(err, &l) {
		return l.Localize(lang)
	}
	return err.Error()
}

// HostError 是主机 / 群晖相关的可本地化错误：Key 对应 HostText 文案，Err 为可选的底层原因。
type HostError struct {
	Key  string
	Args []any
	Err  error
}

// NewHostError 创建可本地化错误。
func NewHostError(key string, err error, args ...any) *HostError {
	return &HostError{Key: key, Args: args, Err: err}
}

func (e *HostError) Error() string { return e.Localize(ZhCN) }

func (e *HostError) Unwrap() error { return e.Err }

// Localize 按语言渲染错误文案（含底层原因）。
func (e *HostError) Localize(lang Lang) string {
	msg := HostText(lang, e.Key, e.Args...)
	if e.Err != nil {
		sep := "："
		if lang == EnUS {
			sep = ": "
		}
		msg += sep + LocalizeErr(lang, e.Err)
	}
	return msg
}
