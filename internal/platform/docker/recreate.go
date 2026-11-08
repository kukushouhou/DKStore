package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/transport"
)

func (a *Adapter) findTargetContainer(ctx context.Context, req platform.UpdateRequest) (containerID string, rawInspect map[string]any, wasRunning bool, err error) {
	var list []containerSummary
	if err := a.getJSON(ctx, "/containers/json?all=1", &list); err != nil {
		return "", nil, false, err
	}
	var matchedID string
	for _, cs := range list {
		if req.Source == platform.SourceCompose || req.Source == platform.SourceDSMProject {
			projName := req.AppName
			if req.Project != nil && req.Project.Name != "" {
				projName = req.Project.Name
			}
			if cs.Labels["com.docker.compose.project"] == projName &&
				cs.Labels["com.docker.compose.service"] == req.ServiceName {
				matchedID = cs.ID
				break
			}
		} else {
			for _, n := range cs.Names {
				clean := strings.TrimPrefix(n, "/")
				if clean == req.ServiceName || clean == req.AppName {
					matchedID = cs.ID
					break
				}
			}
			if matchedID != "" {
				break
			}
		}
	}
	if matchedID == "" {
		return "", nil, false, fmt.Errorf("未找到目标服务容器 (%s/%s)", req.AppName, req.ServiceName)
	}

	raw := make(map[string]any)
	if err := a.getJSON(ctx, "/containers/"+url.PathEscape(matchedID)+"/json", &raw); err != nil {
		return "", nil, false, err
	}
	running := false
	if st, ok := raw["State"].(map[string]any); ok {
		if r, ok := st["Running"].(bool); ok && r {
			running = true
		} else if status, _ := st["Status"].(string); status == "running" {
			running = true
		}
	}
	return matchedID, raw, running, nil
}

// recreateContainerViaEngine 通过 Docker Engine API 原位重建单个容器，完整保留原容器名称、端口、环境变量、挂载卷、网络、重启策略与 Labels，
// 且若原容器处于停止状态则重建后保持停止（立项文档 §7.2）。
func (a *Adapter) recreateContainerViaEngine(
	ctx context.Context,
	execTr transport.ExecutableTransport,
	oldID string,
	rawInspect map[string]any,
	targetImageRef string,
	wasRunning bool,
) (newID string, err error) {
	rawName, _ := rawInspect["Name"].(string)
	origName := strings.TrimPrefix(rawName, "/")
	if origName == "" {
		return "", fmt.Errorf("无法读取原容器名称")
	}

	cfgMap, _ := rawInspect["Config"].(map[string]any)
	if cfgMap == nil {
		return "", fmt.Errorf("无法读取原容器 Config")
	}
	hostCfg, _ := rawInspect["HostConfig"].(map[string]any)

	// 复制 Config 并替换 Image，清理不应带入新容器的 Hostname（若 Hostname 等于旧容器短 ID）
	createPayload := make(map[string]any, len(cfgMap)+2)
	for k, v := range cfgMap {
		createPayload[k] = v
	}
	createPayload["Image"] = targetImageRef
	if hn, _ := createPayload["Hostname"].(string); hn != "" && strings.HasPrefix(oldID, hn) {
		delete(createPayload, "Hostname")
	}
	if hostCfg != nil {
		createPayload["HostConfig"] = hostCfg
	}

	// 提取原容器连接的全部网络（首个网络放入 NetworkingConfig.EndpointsConfig，其余在 create 后 connect）
	type extraNet struct {
		name string
		cfg  map[string]any
	}
	var extraNetworks []extraNet
	if netSettings, ok := rawInspect["NetworkSettings"].(map[string]any); ok {
		if nets, ok := netSettings["Networks"].(map[string]any); ok && len(nets) > 0 {
			firstPlaced := false
			for netName, netVal := range nets {
				epCfg, _ := netVal.(map[string]any)
				if epCfg == nil {
					continue
				}
				// 清理旧容器专属的运行时状态字段（EndpointID / Gateway / IPAddress / MacAddress），保留 Aliases 与 IPAMConfig
				cleanEP := make(map[string]any)
				if aliases, ok := epCfg["Aliases"]; ok {
					cleanEP["Aliases"] = aliases
				}
				if ipam, ok := epCfg["IPAMConfig"]; ok && ipam != nil {
					cleanEP["IPAMConfig"] = ipam
				}
				if !firstPlaced {
					createPayload["NetworkingConfig"] = map[string]any{
						"EndpointsConfig": map[string]any{
							netName: cleanEP,
						},
					}
					firstPlaced = true
				} else {
					extraNetworks = append(extraNetworks, extraNet{name: netName, cfg: cleanEP})
				}
			}
		}
	}

	bodyBytes, err := json.Marshal(createPayload)
	if err != nil {
		return "", err
	}

	// 1. 若原容器正在运行，先优雅停止
	if wasRunning {
		resp, err := execTr.RequestDocker(ctx, http.MethodPost, "/containers/"+url.PathEscape(oldID)+"/stop?t=10", nil, nil)
		if err != nil {
			return "", fmt.Errorf("停止旧容器失败: %w", err)
		}
		_ = resp.Body.Close()
	}

	// 2. 将旧容器临时改名，以便新容器沿用原名；若新容器创建或启动失败可立即恢复旧容器
	backupName := fmt.Sprintf("%s-dkstore-bak-%d", origName, time.Now().Unix()%10000)
	renResp, err := execTr.RequestDocker(ctx, http.MethodPost,
		"/containers/"+url.PathEscape(oldID)+"/rename?name="+url.QueryEscape(backupName), nil, nil)
	if err != nil {
		return "", fmt.Errorf("重命名旧容器失败: %w", err)
	}
	if renResp.StatusCode >= 300 {
		msg := readBodyString(renResp.Body)
		_ = renResp.Body.Close()
		return "", fmt.Errorf("重命名旧容器返回 HTTP %d: %s", renResp.StatusCode, msg)
	}
	_ = renResp.Body.Close()

	restoreOld := func() {
		if r, e := execTr.RequestDocker(ctx, http.MethodPost,
			"/containers/"+url.PathEscape(oldID)+"/rename?name="+url.QueryEscape(origName), nil, nil); e == nil {
			_ = r.Body.Close()
		}
		if wasRunning {
			if r, e := execTr.RequestDocker(ctx, http.MethodPost,
				"/containers/"+url.PathEscape(oldID)+"/start", nil, nil); e == nil {
				_ = r.Body.Close()
			}
		}
	}

	// 3. 创建新容器
	crResp, err := execTr.RequestDocker(ctx, http.MethodPost,
		"/containers/create?name="+url.QueryEscape(origName),
		bytes.NewReader(bodyBytes),
		map[string]string{"Content-Type": "application/json"},
	)
	if err != nil {
		restoreOld()
		return "", fmt.Errorf("创建新容器失败: %w", err)
	}
	if crResp.StatusCode < 200 || crResp.StatusCode >= 300 {
		msg := readBodyString(crResp.Body)
		_ = crResp.Body.Close()
		restoreOld()
		return "", fmt.Errorf("创建新容器返回 HTTP %d: %s", crResp.StatusCode, msg)
	}
	var crRes struct {
		ID string `json:"Id"`
	}
	_ = json.NewDecoder(crResp.Body).Decode(&crRes)
	_ = crResp.Body.Close()
	newID = crRes.ID

	// 4. 连接附加网络
	for _, en := range extraNetworks {
		connBody, _ := json.Marshal(map[string]any{
			"Container":      newID,
			"EndpointConfig": en.cfg,
		})
		if r, e := execTr.RequestDocker(ctx, http.MethodPost,
			"/networks/"+url.PathEscape(en.name)+"/connect",
			bytes.NewReader(connBody),
			map[string]string{"Content-Type": "application/json"},
		); e == nil {
			_ = r.Body.Close()
		}
	}

	// 5. 若原容器原本处于运行状态，启动新容器；若原容器原本处于停止状态，保持停止（立项文档 §7.2）
	if wasRunning {
		stResp, err := execTr.RequestDocker(ctx, http.MethodPost, "/containers/"+url.PathEscape(newID)+"/start", nil, nil)
		if err != nil {
			if r, e := execTr.RequestDocker(ctx, http.MethodDelete, "/containers/"+url.PathEscape(newID)+"?force=1", nil, nil); e == nil {
				_ = r.Body.Close()
			}
			restoreOld()
			return "", fmt.Errorf("启动新容器失败: %w", err)
		}
		if stResp.StatusCode >= 300 {
			msg := readBodyString(stResp.Body)
			_ = stResp.Body.Close()
			if r, e := execTr.RequestDocker(ctx, http.MethodDelete, "/containers/"+url.PathEscape(newID)+"?force=1", nil, nil); e == nil {
				_ = r.Body.Close()
			}
			restoreOld()
			return "", fmt.Errorf("启动新容器返回 HTTP %d: %s", stResp.StatusCode, msg)
		}
		_ = stResp.Body.Close()
	}

	// 6. 新容器已就绪，移除旧备份容器（不删除匿名卷）
	if delResp, err := execTr.RequestDocker(ctx, http.MethodDelete, "/containers/"+url.PathEscape(oldID)+"?v=0", nil, nil); err == nil {
		_ = delResp.Body.Close()
	}
	return newID, nil
}

func (a *Adapter) verifyUpdatedService(
	ctx context.Context,
	req platform.UpdateRequest,
	expectedNewImageID string,
	expectedNewRepoDigest string,
	wasRunning bool,
) (*platform.UpdateResult, error) {
	cid, raw, running, err := a.findTargetContainer(ctx, req)
	if err != nil {
		return nil, err
	}
	actualImageID, _ := raw["Image"].(string)
	if expectedNewImageID != "" && actualImageID != expectedNewImageID {
		return nil, fmt.Errorf("容器运行镜像未切换：期望 %s，实际 %s", shortID(expectedNewImageID), shortID(actualImageID))
	}
	if wasRunning && !running {
		return nil, fmt.Errorf("更新后容器未处于运行状态")
	}

	stateStr := "stopped"
	healthStr := "none"
	if st, ok := raw["State"].(map[string]any); ok {
		if s, ok := st["Status"].(string); ok && s != "" {
			stateStr = s
		}
		if h, ok := st["Health"].(map[string]any); ok {
			if hs, ok := h["Status"].(string); ok && hs != "" {
				healthStr = hs
			}
		}
	}

	// 若容器配置了 Healthcheck 且处于 starting，最多等待 12 秒观察健康状态（立项文档 §7.2）
	if wasRunning && healthStr == "starting" {
		deadline := time.Now().Add(12 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(1500 * time.Millisecond)
			_, latestRaw, _, err := a.findTargetContainer(ctx, req)
			if err != nil {
				break
			}
			if st, ok := latestRaw["State"].(map[string]any); ok {
				if h, ok := st["Health"].(map[string]any); ok {
					if hs, ok := h["Status"].(string); ok {
						healthStr = hs
						if hs == "healthy" {
							break
						}
						if hs == "unhealthy" {
							return nil, fmt.Errorf("容器健康检查未通过 (status=unhealthy)")
						}
					}
				}
			}
		}
	}

	return &platform.UpdateResult{
		NewImageID:     actualImageID,
		NewRepoDigest:  expectedNewRepoDigest,
		NewContainerID: cid,
		ContainerState: stateStr,
		HealthStatus:   healthStr,
	}, nil
}
