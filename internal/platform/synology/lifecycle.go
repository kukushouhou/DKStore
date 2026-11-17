package synology

import (
	"context"
	"strings"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/platform"
)

func (c *dsmClient) StartContainer(ctx context.Context, name string) error {
	return c.call(ctx, "SYNO.Docker.Container", "1", "start", map[string]string{
		"name": jsonParam(name),
	}, nil)
}

func (c *dsmClient) RestartContainer(ctx context.Context, name string) error {
	return c.call(ctx, "SYNO.Docker.Container", "1", "restart", map[string]string{
		"name": jsonParam(name),
	}, nil)
}

func (c *dsmClient) StartProject(ctx context.Context, projectID string) error {
	return c.call(ctx, "SYNO.Docker.Project", "1", "start", map[string]string{
		"id": jsonParam(projectID),
	}, nil)
}

func (c *dsmClient) StopProject(ctx context.Context, projectID string) error {
	return c.call(ctx, "SYNO.Docker.Project", "1", "stop", map[string]string{
		"id": jsonParam(projectID),
	}, nil)
}

func (c *dsmClient) RestartProject(ctx context.Context, projectID string) error {
	if err := c.call(ctx, "SYNO.Docker.Project", "1", "restart", map[string]string{
		"id": jsonParam(projectID),
	}, nil); err == nil {
		return nil
	}
	if err := c.StopProject(ctx, projectID); err != nil {
		return err
	}
	return c.StartProject(ctx, projectID)
}

// ControlLifecycle 在群晖主机上执行单容器或整项目的启动、停止或重启操作（兼容 SSH/Local 与免 SSH 的 DSM WebAPI 模式）。
func (a *Adapter) ControlLifecycle(ctx context.Context, req platform.LifecycleRequest) error {
	if req.Source == platform.SourceVendorManaged {
		return &platform.UpdateError{
			Code:    "vendor_managed_readonly",
			Message: bi("群晖套件中心托管的容器受只读保护", "This container is managed by Synology Package Center and is read-only"),
		}
	}
	if a.dock != nil {
		err := a.dock.ControlLifecycle(ctx, req)
		a.invalidateProjectCache()
		return err
	}
	if a.dsmCli == nil {
		return &platform.UpdateError{Code: "lifecycle_failed", Message: biErr(i18n.NewHostError("dsm.not_configured", nil))}
	}

	action := strings.ToLower(strings.TrimSpace(string(req.Action)))
	isSingleContainer := strings.TrimSpace(req.ContainerID) != "" ||
		strings.TrimSpace(req.ContainerName) != "" ||
		strings.TrimSpace(req.ServiceName) != "" ||
		req.Source == platform.SourceStandalone

	if isSingleContainer {
		cName, err := a.resolveDSMLifecycleContainerName(ctx, req)
		if err != nil {
			return err
		}
		var actErr error
		switch action {
		case string(platform.ActionStart):
			actErr = a.dsmCli.StartContainer(ctx, cName)
		case string(platform.ActionStop):
			actErr = a.dsmCli.StopContainer(ctx, cName)
		case string(platform.ActionRestart):
			actErr = a.dsmCli.RestartContainer(ctx, cName)
		default:
			return &platform.UpdateError{Code: "bad_request", Message: "unsupported action: " + action}
		}
		a.invalidateProjectCache()
		if actErr != nil {
			return &platform.UpdateError{Code: "lifecycle_failed", Message: biErr(actErr)}
		}
		return nil
	}

	// 整项目操作
	if req.Project != nil && strings.TrimSpace(req.Project.DSMProjectID) != "" {
		pid := strings.TrimSpace(req.Project.DSMProjectID)
		var projErr error
		switch action {
		case string(platform.ActionStart):
			projErr = a.dsmCli.StartProject(ctx, pid)
			if projErr != nil {
				// 若项目尚未构建容器，回退调用 buildProject 拉起
				projErr = a.dsmCli.buildProject(ctx, pid)
			}
		case string(platform.ActionStop):
			projErr = a.dsmCli.StopProject(ctx, pid)
		case string(platform.ActionRestart):
			projErr = a.dsmCli.RestartProject(ctx, pid)
		default:
			return &platform.UpdateError{Code: "bad_request", Message: "unsupported action: " + action}
		}
		if projErr == nil {
			a.invalidateProjectCache()
			return nil
		}
	}

	// 回退按项目所属容器逐个执行
	containers, err := a.dsmCli.ListContainers(ctx)
	if err != nil {
		return &platform.UpdateError{Code: "lifecycle_failed", Message: biErr(err)}
	}
	projName := strings.TrimSpace(req.AppName)
	if req.Project != nil && strings.TrimSpace(req.Project.Name) != "" {
		projName = strings.TrimSpace(req.Project.Name)
	}
	var names []string
	for _, c := range containers {
		if strings.EqualFold(strings.TrimSpace(c.Labels["com.docker.compose.project"]), projName) {
			if n := strings.TrimPrefix(strings.TrimSpace(c.Name), "/"); n != "" {
				names = append(names, n)
			}
		}
	}
	if len(names) == 0 {
		return &platform.UpdateError{
			Code:    "lifecycle_failed",
			Message: bi("未找到项目所属容器："+projName, "No containers found for project: "+projName),
		}
	}
	for _, n := range names {
		var actErr error
		switch action {
		case string(platform.ActionStart):
			actErr = a.dsmCli.StartContainer(ctx, n)
		case string(platform.ActionStop):
			actErr = a.dsmCli.StopContainer(ctx, n)
		case string(platform.ActionRestart):
			actErr = a.dsmCli.RestartContainer(ctx, n)
		}
		if actErr != nil {
			a.invalidateProjectCache()
			return &platform.UpdateError{Code: "lifecycle_failed", Message: biErr(actErr)}
		}
	}
	a.invalidateProjectCache()
	return nil
}

func (a *Adapter) resolveDSMLifecycleContainerName(ctx context.Context, req platform.LifecycleRequest) (string, error) {
	wantName := strings.TrimPrefix(strings.TrimSpace(req.ContainerName), "/")
	if wantName != "" {
		return wantName, nil
	}
	wantID := strings.TrimSpace(req.ContainerID)
	wantSvc := strings.TrimSpace(req.ServiceName)
	projName := strings.TrimSpace(req.AppName)
	if req.Project != nil && strings.TrimSpace(req.Project.Name) != "" {
		projName = strings.TrimSpace(req.Project.Name)
	}

	if req.Project != nil && strings.TrimSpace(req.Project.DSMProjectID) != "" {
		if detail, err := a.dsmCli.GetProject(ctx, strings.TrimSpace(req.Project.DSMProjectID)); err == nil && detail != nil {
			for _, c := range detail.Containers {
				if wantID != "" && (c.ID == wantID || strings.HasPrefix(c.ID, wantID) || strings.HasPrefix(wantID, c.ID)) {
					if n := strings.TrimPrefix(c.Name, "/"); n != "" {
						return n, nil
					}
				}
				if wantSvc != "" && c.Config.Labels["com.docker.compose.service"] == wantSvc {
					if n := strings.TrimPrefix(c.Name, "/"); n != "" {
						return n, nil
					}
				}
			}
		}
	}

	containers, err := a.dsmCli.ListContainers(ctx)
	if err != nil {
		return "", &platform.UpdateError{Code: "lifecycle_failed", Message: biErr(err)}
	}
	for _, c := range containers {
		n := strings.TrimPrefix(strings.TrimSpace(c.Name), "/")
		if wantID != "" && (c.ID == wantID || strings.HasPrefix(c.ID, wantID) || strings.HasPrefix(wantID, c.ID)) && n != "" {
			return n, nil
		}
		if wantSvc != "" && (req.Source == platform.SourceCompose || req.Source == platform.SourceDSMProject) {
			if strings.EqualFold(strings.TrimSpace(c.Labels["com.docker.compose.project"]), projName) &&
				strings.TrimSpace(c.Labels["com.docker.compose.service"]) == wantSvc && n != "" {
				return n, nil
			}
		}
		if (wantSvc != "" && n == wantSvc) || (req.AppName != "" && n == req.AppName) {
			return n, nil
		}
	}
	fallback := wantSvc
	if fallback == "" || fallback == "main" {
		fallback = strings.TrimSpace(req.AppName)
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", &platform.UpdateError{
		Code:    "lifecycle_failed",
		Message: bi("未找到目标容器", "Target container not found"),
	}
}
