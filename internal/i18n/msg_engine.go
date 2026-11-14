package i18n

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// 本文件提供更新执行、检测与注册表等“引擎层”返回给用户的可本地化错误码与中英文文案（审计 I1）。
//
// 用法：
//   - 构造错误：i18n.NewEngineError(i18n.MsgUpdateNoService)
//   - 需要以字符串形式落库（如 update_job.error_msg、platform.UpdateError.Message）时：i18n.EngineDetail(key, args...)，
//     读取展示时用 i18n.LocalizeDetail(lang, stored) 还原为当前请求语言；普通字符串原样返回。
//   - 处理任意 error：i18n.LocalizeError(lang, err)。

// 引擎消息键。
const (
	// 更新任务（updater）
	MsgUpdateNoService       = "update.no_service"        // 未选择需要更新的服务
	MsgUpdateServiceNotFound = "update.service_not_found" // 未找到服务: %s
	MsgUpdateJobNotFound     = "update.job_not_found"     // 未找到更新记录: %s
	MsgUpdateAdapterNoUpdate = "update.adapter_no_update" // 平台适配器不支持更新
	MsgUpdatePanic           = "update.panic"             // 更新任务异常中止: %s
	MsgUpdateInterrupted     = "update.interrupted"       // 服务重启导致任务中断
	MsgUpdateRollbackWarning = "update.rollback_warning"  // 回退数据警告
	MsgUpdateTagNotFound     = "update.tag_not_found"     // 仓库中未找到标签: %s
	MsgComposeInvalidYAML    = "compose.invalid_yaml"     // Compose YAML 格式无效: %s
	MsgComposeNotEditable    = "compose.not_editable"     // 当前项目不支持直接编辑配置
	MsgComposeServicesRemoved = "compose.services_removed" // 新配置移除了原有容器: %s
	MsgDeleteAppNotFound     = "delete.app_not_found"     // 未找到目标应用: %s
	MsgDeleteUnsupported     = "delete.unsupported"       // 当前设备连接方式不支持删除操作

	// Docker 引擎更新执行（platform/docker）
	MsgDockerContainerNotFound = "docker.container_not_found" // 未找到目标服务容器 (%s/%s)
	MsgDockerNoContainerName   = "docker.no_container_name"   // 无法读取原容器名称
	MsgDockerNoContainerConfig = "docker.no_container_config" // 无法读取原容器 Config
	MsgDockerStopFailed        = "docker.stop_failed"         // 停止旧容器失败: %s
	MsgDockerRenameFailed      = "docker.rename_failed"       // 重命名旧容器失败: %s
	MsgDockerCreateFailed      = "docker.create_failed"       // 创建新容器失败: %s
	MsgDockerStartFailed       = "docker.start_failed"        // 启动新容器失败: %s
	MsgDockerImageNotSwitched  = "docker.image_not_switched"  // 容器运行镜像未切换：期望 %s，实际 %s
	MsgDockerNotRunning        = "docker.not_running"         // 更新后容器未处于运行状态
	MsgDockerUnhealthy         = "docker.unhealthy"           // 容器健康检查未通过
	MsgDockerInspectPulled     = "docker.inspect_pulled"      // 读取新拉取镜像信息失败: %s
	MsgDockerPullFailed        = "docker.pull_failed"         // 拉取失败: %s
	MsgDockerPullOldZstdVer    = "docker.pull_old_zstd_ver"   // 目标设备 Docker 版本过旧（当前 %s），不支持 zstd
	MsgDockerPullOldZstd       = "docker.pull_old_zstd"       // 目标设备 Docker 版本过旧，不支持 zstd
	MsgDockerVendorReadonly    = "docker.vendor_readonly"     // 厂商托管容器不可直接修改
	MsgDockerNoExec            = "docker.no_exec"             // 传输层不支持更新执行
	MsgDockerImageChanged      = "docker.image_changed"       // 容器镜像已被外部修改 (%s -> %s)
	MsgDockerComposeChanged    = "docker.compose_changed"     // Compose 文件在拉取期间被外部修改
	MsgDockerComposeWrite      = "docker.compose_write"       // 写入 Compose 文件 %s 失败: %s
	MsgDockerComposeRestored   = "docker.compose_restored"    // %s（已写回原 Compose 文件）
	MsgDockerComposeRestoreErr = "docker.compose_restore_err" // %s（写回原 Compose 文件失败: %s）
	MsgDockerComposeRebuildErr = "docker.compose_rebuild_err" // %s（已写回原 Compose 文件，但按原配置恢复运行失败: %s）
	MsgDockerHTTPStatus        = "docker.http_status"         // HTTP %s: %s
	MsgDockerProbeFailed       = "docker.probe_failed"        // 读取 Docker /info 失败: %s
	MsgDockerListFailed        = "docker.list_failed"         // 列出容器失败: %s

	// SSH 传输层（transport）
	MsgTransportHostKeyMismatch   = "transport.host_key_mismatch"   // SSH 主机指纹不匹配：已保存 %s，远端返回 %s
	MsgTransportUntrustedKey      = "transport.untrusted_key"       // 尚未信任该主机指纹（%s）
	MsgTransportTCPFailed         = "transport.tcp_failed"          // SSH TCP 连接失败 (%s): %s
	MsgTransportHandshakeFailed   = "transport.handshake_failed"    // SSH 握手/认证失败: %s
	MsgTransportParseKeyFailed    = "transport.parse_key_failed"    // 解析 SSH 私钥失败: %s
	MsgTransportUnsupportedAuth   = "transport.unsupported_auth"    // 不支持的 SSH 认证方式: %s
	MsgTransportDockerProbeFailed = "transport.docker_probe_failed" // 无法通过 SSH 访问远端 Docker: %s
	MsgTransportUnknownMode       = "transport.unknown_mode"        // 未知传输模式: %s
	MsgTransportInvalidPath       = "transport.invalid_path"        // 文件路径不合法
	MsgTransportReadFileFailed    = "transport.read_file_failed"    // 读取远端文件 %s 失败: %s

	// 镜像仓库（registry）
	MsgRegistryCredIncomplete = "registry.cred_incomplete" // 请填写完整的仓库域名、用户名与密码/Token
	MsgRegistryNetwork        = "registry.network"         // 访问 %s 网络失败: %s
	MsgRegistryAuth           = "registry.auth"            // 仓库 %s 需要认证（HTTP %s）
	MsgRegistryNotFound       = "registry.not_found"       // 仓库 %s 中未找到该镜像或标签
	MsgRegistryRateLimited    = "registry.rate_limited"    // 仓库 %s 限流
	MsgRegistryStatus         = "registry.status"          // 仓库 %s 返回异常状态（HTTP %s）
	MsgRegistryParse          = "registry.parse"           // 解析 %s 响应失败

	// 忽略规则（ignore）
	MsgIgnoreNoService   = "ignore.no_service"   // 未指定要忽略的服务
	MsgIgnoreInvalidMode = "ignore.invalid_mode" // 忽略模式无效
)

var engineMessages = map[string][2]string{
	// [zh-CN, en-US]
	MsgUpdateNoService:       {"未选择需要更新的服务", "No service selected for update"},
	MsgUpdateServiceNotFound: {"未找到服务：%s", "Service not found: %s"},
	MsgUpdateJobNotFound:     {"未找到更新记录：%s", "Update record not found: %s"},
	MsgUpdateAdapterNoUpdate: {"该主机的平台适配器不支持执行更新", "The platform adapter of this host does not support updates"},
	MsgUpdatePanic:           {"更新任务异常中止：%s", "Update task aborted unexpectedly: %s"},
	MsgUpdateInterrupted:     {"DKStore 重启导致任务中断，请确认容器状态后重试", "Task was interrupted by a DKStore restart; please verify the container state and retry"},
	MsgUpdateRollbackWarning: {"注意：回退容器镜像不等于回退业务数据库或持久化卷数据结构；若新版已执行不可逆的数据迁移，请先确认数据兼容性。", "Note: Rolling back the container image does not roll back persistent database schemas or volume data."},
	MsgUpdateTagNotFound:     {"在镜像仓库中未找到指定版本标签：%s", "Specified version tag %s was not found in the image repository"},
	MsgComposeInvalidYAML:     {"Compose 配置格式不正确：%s", "Invalid Compose YAML configuration: %s"},
	MsgComposeNotEditable:     {"当前应用不支持直接编辑 Compose 配置", "Direct Compose configuration editing is not supported for this app"},
	MsgComposeServicesRemoved: {"新配置不再包含原有容器（%s），请先确认移除后再更新", "The new configuration removes existing container(s) (%s); please confirm before updating"},
	MsgDeleteAppNotFound:      {"未找到目标容器或项目：%s", "Target container or project not found: %s"},
	MsgDeleteUnsupported:     {"当前设备的连接方式暂不支持执行删除操作", "The current connection mode of this device does not support deletion"},

	MsgDockerContainerNotFound: {"未找到目标服务容器（%s/%s）", "Target service container not found (%s/%s)"},
	MsgDockerNoContainerName:   {"无法读取原容器名称", "Unable to read the original container name"},
	MsgDockerNoContainerConfig: {"无法读取原容器配置", "Unable to read the original container configuration"},
	MsgDockerStopFailed:        {"停止旧容器失败：%s", "Failed to stop the old container: %s"},
	MsgDockerRenameFailed:      {"重命名旧容器失败：%s", "Failed to rename the old container: %s"},
	MsgDockerCreateFailed:      {"创建新容器失败：%s", "Failed to create the new container: %s"},
	MsgDockerStartFailed:       {"启动新容器失败：%s", "Failed to start the new container: %s"},
	MsgDockerImageNotSwitched:  {"容器运行镜像未切换：期望 %s，实际 %s", "Running image did not switch: expected %s, got %s"},
	MsgDockerNotRunning:        {"更新后容器未处于运行状态", "Container is not running after the update"},
	MsgDockerUnhealthy:         {"容器健康检查未通过（unhealthy）", "Container health check failed (unhealthy)"},
	MsgDockerInspectPulled:     {"读取新拉取镜像信息失败：%s", "Failed to inspect the pulled image: %s"},
	MsgDockerPullFailed:        {"%s", "%s"},
	MsgDockerPullOldZstdVer:    {"目标设备 Docker 版本过旧（当前 %s），不支持解压新版镜像的 zstd 压缩层，请先升级该设备的 Docker（需 ≥ 20.10.10）", "Docker Engine on the target host is too old (currently %s) to extract zstd-compressed image layers; please upgrade Docker on that host to 20.10.10 or newer"},
	MsgDockerPullOldZstd:       {"目标设备 Docker 版本过旧，不支持解压新版镜像的 zstd 压缩层，请先升级该设备的 Docker（需 ≥ 20.10.10）", "Docker Engine on the target host is too old to extract zstd-compressed image layers; please upgrade Docker on that host to 20.10.10 or newer"},
	MsgDockerVendorReadonly:    {"该容器由系统套件托管，不能直接修改", "This container is managed by the vendor package and cannot be modified directly"},
	MsgDockerNoExec:            {"当前连接方式不支持执行更新", "The current connection does not support update execution"},
	MsgDockerImageChanged:      {"容器镜像已被外部修改（%s → %s）", "Container image was changed externally (%s → %s)"},
	MsgDockerComposeChanged:    {"拉取镜像期间 Compose 文件被外部修改", "Compose file was modified externally during the image pull"},
	MsgDockerComposeWrite:      {"写入 Compose 文件 %s 失败：%s", "Failed to write Compose file %s: %s"},
	MsgDockerComposeRestored:   {"%s（已写回原 Compose 文件）", "%s (original Compose file restored)"},
	MsgDockerComposeRestoreErr: {"%s（写回原 Compose 文件失败：%s）", "%s (failed to restore the original Compose file: %s)"},
	MsgDockerComposeRebuildErr: {"%s（已写回原 Compose 文件，但按原配置恢复运行失败：%s）", "%s (original Compose file restored, but rebuilding with it failed: %s)"},
	MsgDockerHTTPStatus:        {"Docker 返回 HTTP %s：%s", "Docker returned HTTP %s: %s"},
	MsgDockerProbeFailed:       {"读取 Docker /info 失败：%s", "Failed to read Docker /info: %s"},
	MsgDockerListFailed:        {"列出容器失败：%s", "Failed to list containers: %s"},

	MsgTransportHostKeyMismatch:   {"SSH 主机指纹不匹配：已保存 %s，远端返回 %s", "SSH host fingerprint mismatch: expected %s, got %s"},
	MsgTransportUntrustedKey:      {"尚未信任该主机指纹（%s）", "Host fingerprint is not trusted yet (%s)"},
	MsgTransportTCPFailed:         {"SSH TCP 连接失败（%s）：%s", "SSH TCP connection failed (%s): %s"},
	MsgTransportHandshakeFailed:   {"SSH 握手/认证失败：%s", "SSH handshake or authentication failed: %s"},
	MsgTransportParseKeyFailed:    {"解析 SSH 私钥失败：%s", "Failed to parse SSH private key: %s"},
	MsgTransportUnsupportedAuth:   {"不支持的 SSH 认证方式：%s", "Unsupported SSH authentication method: %s"},
	MsgTransportDockerProbeFailed: {"无法通过 SSH 访问远端 Docker（已尝试直连 Socket、dial-stdio、sudo dial-stdio）：%s", "Unable to access remote Docker over SSH (tried direct socket, dial-stdio, sudo dial-stdio): %s"},
	MsgTransportUnknownMode:       {"未知传输模式：%s", "Unknown transport mode: %s"},
	MsgTransportInvalidPath:       {"文件路径不合法", "Invalid file path"},
	MsgTransportReadFileFailed:    {"读取远端文件 %s 失败：%s", "Failed to read remote file %s: %s"},

	MsgRegistryCredIncomplete: {"请填写完整的仓库域名、用户名与密码/Token", "Please fill in the registry host, username and password/token"},
	MsgRegistryNetwork:        {"访问 %s 网络失败：%s", "Network error while contacting %s: %s"},
	MsgRegistryAuth:           {"仓库 %s 需要认证或无权限访问（HTTP %s）", "Registry %s requires authentication or denied access (HTTP %s)"},
	MsgRegistryNotFound:       {"仓库 %s 中未找到该镜像或标签（HTTP 404）", "Image or tag not found in registry %s (HTTP 404)"},
	MsgRegistryRateLimited:    {"仓库 %s 请求触发限流（HTTP 429），请稍后重试或配置仓库账号", "Registry %s rate limit exceeded (HTTP 429); retry later or configure an account"},
	MsgRegistryStatus:         {"仓库 %s 返回异常状态（HTTP %s）", "Registry %s returned an unexpected status (HTTP %s)"},
	MsgRegistryParse:          {"解析 %s 响应失败", "Failed to parse the response from %s"},

	MsgIgnoreNoService:   {"未指定要忽略的服务", "No service specified to ignore"},
	MsgIgnoreInvalidMode: {"忽略模式无效，仅支持 this_version、prefix 或 forever", "Invalid ignore mode; only this_version, prefix, or forever is supported"},
}

// EngineMessage 按语言渲染引擎消息；未知键原样返回键名。
func EngineMessage(lang Lang, key string, args ...string) string {
	pair, ok := engineMessages[key]
	if !ok {
		if len(args) > 0 {
			return key + ": " + strings.Join(args, ", ")
		}
		return key
	}
	tmpl := pair[0]
	if lang == EnUS {
		tmpl = pair[1]
	}
	if len(args) == 0 {
		return tmpl
	}
	vals := make([]any, len(args))
	for i, a := range args {
		vals[i] = LocalizeDetail(lang, a)
	}
	return fmt.Sprintf(tmpl, vals...)
}

// EngineError 是携带可本地化消息键的错误。
type EngineError struct {
	Key  string
	Args []string
}

// NewEngineError 创建可本地化错误；args 可以是普通字符串或 EngineDetail 编码的嵌套消息。
func NewEngineError(key string, args ...string) *EngineError {
	return &EngineError{Key: key, Args: args}
}

// Error 返回默认语言（zh-CN）文案，保证未经本地化处理时也不会出现中英混杂。
func (e *EngineError) Error() string { return EngineMessage(ZhCN, e.Key, e.Args...) }

// Localize 按语言渲染。
func (e *EngineError) Localize(lang Lang) string { return EngineMessage(lang, e.Key, e.Args...) }

// Detail 返回可落库的编码字符串。
func (e *EngineError) Detail() string { return EngineDetail(e.Key, e.Args...) }

const detailPrefix = "@msg:"

type encodedDetail struct {
	K string   `json:"k"`
	A []string `json:"a,omitempty"`
}

// EngineDetail 将消息键与参数编码为可存储字符串，读取时通过 LocalizeDetail 按语言还原。
func EngineDetail(key string, args ...string) string {
	b, _ := json.Marshal(encodedDetail{K: key, A: args})
	return detailPrefix + string(b)
}

// DetailOf 将任意 error 转换为可落库的字符串（EngineError 编码，其余取 Error()）。
func DetailOf(err error) string {
	if err == nil {
		return ""
	}
	var ee *EngineError
	if errors.As(err, &ee) {
		return ee.Detail()
	}
	return err.Error()
}

// LocalizeDetail 将 EngineDetail 编码的字符串按语言还原；普通字符串原样返回。
func LocalizeDetail(lang Lang, s string) string {
	if !strings.HasPrefix(s, detailPrefix) {
		return s
	}
	var d encodedDetail
	if err := json.Unmarshal([]byte(s[len(detailPrefix):]), &d); err != nil || d.K == "" {
		return s
	}
	return EngineMessage(lang, d.K, d.A...)
}

// LocalizeError 按语言渲染任意 error（EngineError 与编码字符串按语言还原，其余原样）。
func LocalizeError(lang Lang, err error) string {
	if err == nil {
		return ""
	}
	var ee *EngineError
	if errors.As(err, &ee) {
		return ee.Localize(lang)
	}
	return LocalizeDetail(lang, err.Error())
}
