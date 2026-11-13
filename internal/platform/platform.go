// Package platform 定义被管理平台适配器的公共接口与模型（实施文档 §2.2）。
//
// 每种被管理平台（原生 Docker、群晖 Container Manager……）实现 Adapter；
// 核心只依赖本包，不感知具体平台，从而保证“安装位置与管理范围解耦”。
package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
)

// SourceKind 标识应用的部署来源。
type SourceKind string

const (
	SourceStandalone    SourceKind = "standalone"     // 独立容器
	SourceCompose       SourceKind = "compose"        // 普通 Compose 项目
	SourceDSMProject    SourceKind = "dsm_project"    // 群晖 Container Manager 原生项目
	SourceVendorManaged SourceKind = "vendor_managed" // 厂商套件管理的容器
)

// Adapter 是被管理平台适配器。
type Adapter interface {
	Kind() string
	Probe(ctx context.Context) (*HostInfo, error)
	ListApps(ctx context.Context) ([]App, error)
	GetProjectCompose(ctx context.Context, project *ProjectInfo) (string, error)
	UpdateProjectCompose(ctx context.Context, appName string, project *ProjectInfo, newYAML string, onProgress func(PullProgress)) error
	Close() error
}

// UpdateCapableAdapter 扩展 Adapter，提供镜像分层拉取、冲突检测与单服务安全重建能力（立项文档 §7 / §8 / §9）。
type UpdateCapableAdapter interface {
	Adapter
	UpdateService(ctx context.Context, req UpdateRequest, onProgress func(PullProgress)) (*UpdateResult, error)
	HasLocalImage(ctx context.Context, imageIDOrRef string) bool
}

// ContainerRuntimeStat 描述单个容器的实时运行状态与资源占用。
type ContainerRuntimeStat struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Service    string  `json:"service,omitempty"`
	Image      string  `json:"image"`
	State      string  `json:"state"`            // running | exited | stopped | restarting | paused
	Health     string  `json:"health,omitempty"` // healthy | unhealthy | starting | ""
	Uptime     string  `json:"uptime,omitempty"` // 如 "Up 3 days" 或基于 StartedAt 计算的时长秒数
	StartedAt  int64   `json:"startedAt,omitempty"`
	CPUPercent float64 `json:"cpuPercent"` // 0-100+ 百分比（保留 1-2 位小数）
	MemUsage   int64   `json:"memUsage"`   // 字节数
	MemLimit   int64   `json:"memLimit"`   // 字节数
}

// AppRuntimeTree 描述应用（多容器项目或独立容器）的树状实时运行状态。
type AppRuntimeTree struct {
	Key          string                 `json:"key"`
	HostID       string                 `json:"hostId"`
	HostName     string                 `json:"hostName"`
	Name         string                 `json:"name"`
	Source       SourceKind             `json:"source"`
	RunningCount int                    `json:"runningCount"`
	TotalCount   int                    `json:"totalCount"`
	CPUPercent   float64                `json:"cpuPercent"`
	MemUsage     int64                  `json:"memUsage"`
	StatsReady   bool                   `json:"statsReady"`
	Containers   []ContainerRuntimeStat `json:"containers"`
}

// RuntimeStatAdapter 扩展 Adapter，提供容器树状实时运行状态采集。
type RuntimeStatAdapter interface {
	Adapter
	ListRuntimeApps(ctx context.Context) ([]AppRuntimeTree, error)
}

// RuntimeStatOptionsAdapter 扩展 RuntimeStatAdapter，支持按需跳过资源占用采样以秒级返回容器列表。
type RuntimeStatOptionsAdapter interface {
	RuntimeStatAdapter
	ListRuntimeAppsWithOptions(ctx context.Context, withStats bool) ([]AppRuntimeTree, error)
}

// LayerProgress 描述单个镜像层的实时下载/解压状态与进度（附录 E.1）。
type LayerProgress struct {
	ID      string `json:"id"`
	Status  string `json:"status"`  // waiting | downloading | extracting | done
	Current int64  `json:"current"`
	Total   int64  `json:"total"`
	Percent int    `json:"percent"` // 0-100
}

// PullProgress 描述更新执行中的实时进度（分层下载字节数/总字节数/百分比、逐层详情与原始终端流，立项文档 §7.3 / 附录 E）。
type PullProgress struct {
	Phase           string          `json:"phase"`              // precheck | pulling | recreating | verifying | done
	StatusText      string          `json:"statusText"`         // 阶段可读描述
	TotalLayers     int             `json:"totalLayers"`        // 已知镜像层总数
	CompletedLayers int             `json:"completedLayers"`    // 已完成层数
	DownloadedBytes int64           `json:"downloadedBytes"`    // 已下载字节数
	TotalBytes      int64           `json:"totalBytes"`         // 已知总字节数
	Percent         int             `json:"percent"`            // 0-100（单调递增）
	Layers          []LayerProgress `json:"layers,omitempty"`   // 各镜像层实时状态
	LogChunk        string          `json:"logChunk,omitempty"` // 原始终端增量输出（供 @xterm/xterm 实时渲染）
}

// EnvVar 表示独立容器的单个环境变量键值对。
type EnvVar struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// PortBinding 表示独立容器的单条端口映射规则。
type PortBinding struct {
	HostIP        string `json:"hostIp,omitempty"`
	HostPort      string `json:"hostPort"`
	ContainerPort string `json:"containerPort"`
	Protocol      string `json:"protocol"` // tcp | udp
}

// UnmarshalJSON 兼容前端传入字符串端口（"8080"）或数字端口（8080）。
func (p *PortBinding) UnmarshalJSON(data []byte) error {
	var raw struct {
		HostIP        string          `json:"hostIp"`
		HostPort      json.RawMessage `json:"hostPort"`
		ContainerPort json.RawMessage `json:"containerPort"`
		Protocol      string          `json:"protocol"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	p.HostIP = strings.TrimSpace(raw.HostIP)
	p.HostPort = decodeFlexPort(raw.HostPort)
	p.ContainerPort = decodeFlexPort(raw.ContainerPort)
	proto := strings.ToLower(strings.TrimSpace(raw.Protocol))
	if proto == "" {
		proto = "tcp"
	}
	p.Protocol = proto
	return nil
}

func decodeFlexPort(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		if n <= 0 {
			return ""
		}
		return strconv.FormatInt(n, 10)
	}
	return strings.Trim(string(raw), `" `)
}

// VolumeBinding 表示独立容器的挂载目录映射（只读展示）。
type VolumeBinding struct {
	HostPath      string `json:"hostPath"`
	ContainerPath string `json:"containerPath"`
	Mode          string `json:"mode"` // rw | ro
}

// StandaloneConfig 描述非 YAML 独立容器的当前运行参数（附录 E.4）。
type StandaloneConfig struct {
	Editable bool            `json:"editable"`
	Reason   string          `json:"reason,omitempty"`
	ImageRef string          `json:"imageRef"`
	Env      []EnvVar        `json:"env"`
	Ports    []PortBinding   `json:"ports"`
	Volumes  []VolumeBinding `json:"volumes"`
}

// StandaloneUpdateRequest 是对独立容器执行镜像切换并同步更新环境变量/端口映射的输入参数（附录 E.4）。
type StandaloneUpdateRequest struct {
	AppName         string
	ServiceName     string
	ExpectedImageID string
	OldImageRef     string
	TargetImageRef  string
	RegistryAuthB64 string
	Env             []EnvVar
	Ports           []PortBinding
}

// StandaloneConfigAdapter 扩展 Adapter，提供无 YAML 独立容器的配置读取与带配置回滚保护的重建能力（附录 E.4）。
type StandaloneConfigAdapter interface {
	Adapter
	GetStandaloneConfig(ctx context.Context, appName, serviceName string) (*StandaloneConfig, error)
	UpdateStandaloneConfig(ctx context.Context, req StandaloneUpdateRequest, onProgress func(PullProgress)) (*UpdateResult, error)
}

// DeleteFileKind 标识可随应用清理的关联文件或卷类型。
type DeleteFileKind string

const (
	DeleteFileProjectDir DeleteFileKind = "project_dir" // Compose 项目配置目录或配置文件
	DeleteFileVolume     DeleteFileKind = "volume"      // Docker 命名或匿名数据卷
	DeleteFileBindMount  DeleteFileKind = "bind_mount"  // 宿主机挂载目录或文件
)

// DeleteFileItem 描述单个关联路径或数据卷项。
type DeleteFileItem struct {
	ID            string         `json:"id"`
	Kind          DeleteFileKind `json:"kind"`
	Path          string         `json:"path"`
	Target        string         `json:"target,omitempty"`
	DefaultSelect bool           `json:"defaultSelect"`
	Protected     bool           `json:"protected"`
	Reason        string         `json:"reason,omitempty"` // system_protected | external_bind | readonly_bind | dsm_api_host_path_unsupported
}

// DeleteAppPreview 是删除容器或整项目前展示的关联资源预览。
type DeleteAppPreview struct {
	AppKey     string           `json:"appKey"`
	AppName    string           `json:"appName"`
	Source     SourceKind       `json:"source"`
	IsProject  bool             `json:"isProject"`
	Containers []string         `json:"containers"`
	Images     []string         `json:"images"`
	Files      []DeleteFileItem `json:"files"`
}

// DeleteAppRequest 是删除单个独立容器或整个项目的执行请求。
type DeleteAppRequest struct {
	AppKey       string
	AppName      string
	Source       SourceKind
	Project      *ProjectInfo
	DeleteImages bool
	FileIDs      []string
}

// DeleteAppResult 汇总删除容器/项目及清理镜像、关联文件的执行结果。
type DeleteAppResult struct {
	RemovedContainers int      `json:"removedContainers"`
	RemovedImages     int      `json:"removedImages"`
	SkippedImages     []string `json:"skippedImages,omitempty"`
	RemovedFiles      int      `json:"removedFiles"`
	Warnings          []string `json:"warnings,omitempty"`
}

// AppDeleteAdapter 扩展 Adapter，提供独立容器与整项目的删除预览及执行能力。
type AppDeleteAdapter interface {
	Adapter
	PreviewDeleteApp(ctx context.Context, appName string, source SourceKind, project *ProjectInfo) (*DeleteAppPreview, error)
	DeleteApp(ctx context.Context, req DeleteAppRequest) (*DeleteAppResult, error)
}

// UpdateRequest 是对单个服务执行升级或回退的输入参数。
type UpdateRequest struct {
	AppName         string
	ServiceName     string
	Source          SourceKind
	Project         *ProjectInfo
	ExpectedImageID string // 执行前容器中运行的 ImageID（用于检测外部并发修改冲突）
	OldImageRef     string // 更新前声明的镜像引用
	TargetImageRef  string // 目标镜像引用（如 nginx:1.27.4 或 ghcr.io/xxx:v1.2.0）
	RegistryAuthB64 string // 可选的 Docker X-Registry-Auth 头
}

// UpdateResult 是单服务更新完成后的核验结果。
type UpdateResult struct {
	NewImageID     string   `json:"newImageId"`
	NewRepoDigest  string   `json:"newRepoDigest"`
	NewContainerID string   `json:"newContainerId"`
	ContainerState string   `json:"containerState"`
	HealthStatus   string   `json:"healthStatus,omitempty"` // healthy | starting | none
	ComposePath    string   `json:"composePath,omitempty"`
	ComposeSHA256  string   `json:"composeSha256,omitempty"`
	Logs           []string `json:"logs,omitempty"`
}

// UpdateError 携带标准化的错误码（如 external_conflict / vendor_managed_readonly / pull_failed / recreate_failed / verify_failed）。
type UpdateError struct {
	Code    string
	Message string
}

func (e *UpdateError) Error() string { return e.Message }

// HostInfo 是主机探测结果。
type HostInfo struct {
	OS              string       `json:"os"`
	Arch            string       `json:"arch"`
	Variant         string       `json:"variant,omitempty"`
	DockerVersion   string       `json:"dockerVersion"`
	PlatformVersion string       `json:"platformVersion,omitempty"`
	Capabilities    Capabilities `json:"capabilities"`
}

// Capabilities 是能力声明。更新相关字段第一阶段只探测、不执行。
type Capabilities struct {
	Discover            bool `json:"discover"`
	CheckUpdate         bool `json:"checkUpdate"`
	NativeProjects      bool `json:"nativeProjects"`
	PullImage           bool `json:"pullImage"`
	UpdateSingleService bool `json:"updateSingleService"`
	UpdateWholeProject  bool `json:"updateWholeProject"`
	NativeManagerSync   bool `json:"nativeManagerSync"`
	Rollback            bool `json:"rollback"`
}

// ProjectInfo 描述 Compose / DSM 项目来源。
type ProjectInfo struct {
	Name           string   `json:"name"`
	DSMProjectID   string   `json:"dsmProjectId,omitempty"`
	WorkingDir     string   `json:"workingDir,omitempty"`
	ConfigFiles    []string `json:"configFiles,omitempty"`
	EnvFiles       []string `json:"envFiles,omitempty"`
	SourceReadable bool     `json:"sourceReadable"`
}

// App 是首页卡片单位：一个 Compose 项目或一个独立容器。
type App struct {
	Key      string       `json:"key"`
	Name     string       `json:"name"`
	Source   SourceKind   `json:"source"`
	Project  *ProjectInfo `json:"project,omitempty"`
	Services []Service    `json:"services"`
}

// Service 是 Compose 服务；独立容器视为只有一个 Service 的 App。
type Service struct {
	Key          string            `json:"key"`
	Name         string            `json:"name"`
	ContainerIDs []string          `json:"containerIds"`
	State        string            `json:"state"`
	ImageRef     string            `json:"imageRef"`
	ImageID      string            `json:"imageId"`
	RepoDigests  []string          `json:"repoDigests"`
	Labels       map[string]string `json:"labels,omitempty"`
}
