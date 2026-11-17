package platform

import "context"

// LifecycleAction 标识容器或项目的启停控制动作。
type LifecycleAction string

const (
	ActionStart   LifecycleAction = "start"
	ActionStop    LifecycleAction = "stop"
	ActionRestart LifecycleAction = "restart"
)

// LifecycleRequest 描述对单个容器或整个项目执行启动、停止或重启的请求参数。
type LifecycleRequest struct {
	AppKey        string
	AppName       string
	Source        SourceKind
	Project       *ProjectInfo
	ContainerID   string
	ContainerName string
	ServiceName   string
	Action        LifecycleAction
}

// LifecycleAdapter 扩展 Adapter，提供容器与整项目的启动、停止和重启能力。
type LifecycleAdapter interface {
	Adapter
	ControlLifecycle(ctx context.Context, req LifecycleRequest) error
}
