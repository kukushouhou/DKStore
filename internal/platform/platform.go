// Package platform 定义被管理平台适配器的公共接口与模型（实施文档 §2.2）。
//
// 每种被管理平台（原生 Docker、群晖 Container Manager……）实现 Adapter；
// 核心只依赖本包，不感知具体平台，从而保证“安装位置与管理范围解耦”。
package platform

import "context"

// SourceKind 标识应用的部署来源。
type SourceKind string

const (
	SourceStandalone    SourceKind = "standalone"     // 独立容器
	SourceCompose       SourceKind = "compose"        // 普通 Compose 项目
	SourceDSMProject    SourceKind = "dsm_project"    // 群晖 Container Manager 原生项目
	SourceVendorManaged SourceKind = "vendor_managed" // 厂商套件管理的容器
)

// Adapter 是被管理平台适配器。第一阶段只读：只做探测与发现。
type Adapter interface {
	Kind() string
	Probe(ctx context.Context) (*HostInfo, error)
	ListApps(ctx context.Context) ([]App, error)
	Close() error
}

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
