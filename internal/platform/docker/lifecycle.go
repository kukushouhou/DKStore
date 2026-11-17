package docker

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/transport"
)

func (a *Adapter) listRunningComposeServiceNames(ctx context.Context, projectName string, filterAllowed []string) []string {
	if strings.TrimSpace(projectName) == "" {
		return nil
	}
	var list []containerSummary
	if err := a.getJSON(ctx, "/containers/json?all=1", &list); err != nil {
		return nil
	}
	allowed := make(map[string]bool, len(filterAllowed))
	for _, s := range filterAllowed {
		allowed[strings.TrimSpace(s)] = true
	}
	var out []string
	seen := make(map[string]bool)
	for _, cs := range list {
		if !strings.EqualFold(strings.TrimSpace(cs.Labels["com.docker.compose.project"]), projectName) {
			continue
		}
		if strings.ToLower(strings.TrimSpace(cs.State)) != "running" {
			continue
		}
		svc := strings.TrimSpace(cs.Labels["com.docker.compose.service"])
		if svc == "" || seen[svc] {
			continue
		}
		if len(allowed) > 0 && !allowed[svc] {
			continue
		}
		seen[svc] = true
		out = append(out, svc)
	}
	return out
}

// ControlLifecycle 在 Docker 主机上执行单容器或整项目的启动、停止或重启操作。
func (a *Adapter) ControlLifecycle(ctx context.Context, req platform.LifecycleRequest) error {
	if req.Source == platform.SourceVendorManaged {
		return &platform.UpdateError{
			Code:    "vendor_managed_readonly",
			Message: i18n.EngineDetail(i18n.MsgDockerVendorReadonly),
		}
	}
	execTr, ok := a.tr.(transport.ExecutableTransport)
	if !ok {
		return &platform.UpdateError{
			Code:    "capability_unsupported",
			Message: i18n.EngineDetail(i18n.MsgDockerNoExec),
		}
	}

	action := strings.ToLower(strings.TrimSpace(string(req.Action)))
	switch action {
	case string(platform.ActionStart), string(platform.ActionStop), string(platform.ActionRestart):
	default:
		return &platform.UpdateError{Code: "bad_request", Message: "unsupported lifecycle action: " + action}
	}

	isSingleContainer := strings.TrimSpace(req.ContainerID) != "" ||
		strings.TrimSpace(req.ContainerName) != "" ||
		strings.TrimSpace(req.ServiceName) != "" ||
		req.Source == platform.SourceStandalone

	if isSingleContainer {
		cid, err := a.findLifecycleContainerID(ctx, req)
		if err != nil {
			return err
		}
		return a.execEngineContainerAction(ctx, execTr, cid, action)
	}

	// 整项目操作（Compose / DSM Project）
	projName := strings.TrimSpace(req.AppName)
	if req.Project != nil && strings.TrimSpace(req.Project.Name) != "" {
		projName = strings.TrimSpace(req.Project.Name)
	}

	var list []containerSummary
	_ = a.getJSON(ctx, "/containers/json?all=1", &list)
	var projContainerIDs []string
	for _, cs := range list {
		if strings.EqualFold(strings.TrimSpace(cs.Labels["com.docker.compose.project"]), projName) {
			projContainerIDs = append(projContainerIDs, cs.ID)
		}
	}

	if lcTr, ok := execTr.(transport.ComposeLifecycleTransport); ok && req.Project != nil && (len(req.Project.ConfigFiles) > 0 || req.Project.WorkingDir != "") {
		if action == string(platform.ActionStart) && len(projContainerIDs) == 0 {
			if _, err := lcTr.ExecComposeAction(ctx, req.Project.WorkingDir, req.Project.ConfigFiles, req.Project.EnvFiles, projName, "up", []string{"-d", "--no-build"}); err == nil {
				return nil
			}
		} else if _, err := lcTr.ExecComposeAction(ctx, req.Project.WorkingDir, req.Project.ConfigFiles, req.Project.EnvFiles, projName, action, nil); err == nil {
			return nil
		}
	}

	if len(projContainerIDs) == 0 {
		return i18n.NewEngineError(i18n.MsgDockerContainerNotFound, req.AppName, projName)
	}
	for _, cid := range projContainerIDs {
		if err := a.execEngineContainerAction(ctx, execTr, cid, action); err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) findLifecycleContainerID(ctx context.Context, req platform.LifecycleRequest) (string, error) {
	var list []containerSummary
	if err := a.getJSON(ctx, "/containers/json?all=1", &list); err != nil {
		return "", err
	}
	wantID := strings.TrimSpace(req.ContainerID)
	wantName := strings.TrimPrefix(strings.TrimSpace(req.ContainerName), "/")
	wantSvc := strings.TrimSpace(req.ServiceName)
	projName := strings.TrimSpace(req.AppName)
	if req.Project != nil && strings.TrimSpace(req.Project.Name) != "" {
		projName = strings.TrimSpace(req.Project.Name)
	}

	for _, cs := range list {
		if wantID != "" && (cs.ID == wantID || strings.HasPrefix(cs.ID, wantID) || strings.HasPrefix(wantID, cs.ID)) {
			return cs.ID, nil
		}
		if wantName != "" {
			for _, n := range cs.Names {
				if strings.TrimPrefix(n, "/") == wantName {
					return cs.ID, nil
				}
			}
		}
	}

	for _, cs := range list {
		if (req.Source == platform.SourceCompose || req.Source == platform.SourceDSMProject) && wantSvc != "" {
			if strings.EqualFold(strings.TrimSpace(cs.Labels["com.docker.compose.project"]), projName) &&
				strings.TrimSpace(cs.Labels["com.docker.compose.service"]) == wantSvc {
				return cs.ID, nil
			}
		}
		for _, n := range cs.Names {
			clean := strings.TrimPrefix(n, "/")
			if (wantSvc != "" && clean == wantSvc) || (req.AppName != "" && clean == req.AppName) {
				return cs.ID, nil
			}
		}
	}

	targetDesc := wantName
	if targetDesc == "" {
		targetDesc = wantSvc
	}
	if targetDesc == "" {
		targetDesc = req.AppName
	}
	return "", i18n.NewEngineError(i18n.MsgDockerContainerNotFound, req.AppName, targetDesc)
}

func (a *Adapter) execEngineContainerAction(ctx context.Context, execTr transport.ExecutableTransport, cid, action string) error {
	var path string
	switch action {
	case string(platform.ActionStart):
		path = "/containers/" + url.PathEscape(cid) + "/start"
	case string(platform.ActionStop):
		path = "/containers/" + url.PathEscape(cid) + "/stop?t=10"
	case string(platform.ActionRestart):
		path = "/containers/" + url.PathEscape(cid) + "/restart?t=10"
	default:
		return &platform.UpdateError{Code: "bad_request", Message: "unsupported action"}
	}

	resp, err := execTr.RequestDocker(ctx, http.MethodPost, path, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 204 成功，304 表示已处于目标状态（如已启动或已停止），均视为成功
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotModified {
		msg := readBodyString(resp.Body)
		return &platform.UpdateError{
			Code:    "lifecycle_failed",
			Message: dockerHTTPErr(resp.StatusCode, msg),
		}
	}
	return nil
}
