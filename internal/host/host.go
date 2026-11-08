// Package host 负责被管理主机的增删改查、连接测试、指纹信任与适配器构造（实施文档 §3）。
package host

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/platform/docker"
	"github.com/kukushouhou/DKStore/internal/secret"
	"github.com/kukushouhou/DKStore/internal/store"
	"github.com/kukushouhou/DKStore/internal/transport"
)

// Host 是对外展示与持久化的主机记录（不含任何密码或私钥明文）。
type Host struct {
	ID            string             `json:"id"`
	Name          string             `json:"name"`
	Kind          string             `json:"kind"`          // docker | synology
	Transport     string             `json:"transport"`     // local | ssh
	Address       string             `json:"address,omitempty"`
	Port          int                `json:"port,omitempty"`
	SSHUser       string             `json:"sshUser,omitempty"`
	SSHAuth       string             `json:"sshAuth,omitempty"` // password | key
	SSHHostKey    string             `json:"sshHostKey,omitempty"`
	TransportMode string             `json:"transportMode,omitempty"`
	DSMURL        string             `json:"dsmUrl,omitempty"`
	DSMUser       string             `json:"dsmUser,omitempty"`
	Info          *platform.HostInfo `json:"info,omitempty"`
	Status        string             `json:"status"` // online | offline | unknown
	LastSeenAt    int64              `json:"lastSeenAt,omitempty"`
	Sort          int                `json:"sort"`
	CreatedAt     int64              `json:"createdAt"`
}

// CreateOrUpdateInput 是新建或编辑主机时的输入。
type CreateOrUpdateInput struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"`      // docker | synology
	Transport     string `json:"transport"` // local | ssh
	Address       string `json:"address"`
	Port          int    `json:"port"`
	SSHUser       string `json:"sshUser"`
	SSHAuth       string `json:"sshAuth"`   // password | key
	SSHSecret     string `json:"sshSecret"` // 密码或私钥 PEM；编辑时留空表示不修改
	SSHPassphrase string `json:"sshPassphrase"`
	SSHHostKey    string `json:"sshHostKey"` // 连接测试确认后的指纹
	DSMURL        string `json:"dsmUrl"`
	DSMUser       string `json:"dsmUser"`
	DSMSecret     string `json:"dsmSecret"`
}

// StepResult 表示连接测试中单步的诊断结果。
type StepResult struct {
	Step    string `json:"step"`
	Status  string `json:"status"` // ok | error | skipped
	Message string `json:"message"`
}

// TestResult 是 POST /hosts/test 的返回结构。
type TestResult struct {
	OK               bool               `json:"ok"`
	Steps            []StepResult       `json:"steps"`
	PresentedHostKey string             `json:"presentedHostKey,omitempty"`
	HostKeyTrusted   bool               `json:"hostKeyTrusted"`
	TransportMode    string             `json:"transportMode,omitempty"`
	Info             *platform.HostInfo `json:"info,omitempty"`
}

// Registry 管理主机与凭证。
type Registry struct {
	st  *store.Store
	box *secret.Box
}

// NewRegistry 创建主机注册表。
func NewRegistry(st *store.Store, box *secret.Box) *Registry {
	return &Registry{st: st, box: box}
}

// EnsureLocalHost 在主机表为空且本机存在 /var/run/docker.sock 时，自动添加一条“本机 Docker”记录。
func (r *Registry) EnsureLocalHost(ctx context.Context) error {
	var count int
	if err := r.st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM host`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	sock := transport.DefaultLocalSocket()
	if _, err := os.Stat(sock); err != nil {
		return nil
	}
	_, err := r.Create(ctx, CreateOrUpdateInput{
		Name:      "本机 Docker",
		Kind:      "docker",
		Transport: "local",
	})
	return err
}

// List 返回所有主机。
func (r *Registry) List(ctx context.Context) ([]Host, error) {
	rows, err := r.st.DB.QueryContext(ctx, `
		SELECT id, name, kind, transport, COALESCE(address,''), COALESCE(port,0),
		       COALESCE(ssh_user,''), COALESCE(ssh_auth,''), COALESCE(ssh_host_key,''),
		       COALESCE(transport_mode,''), COALESCE(dsm_url,''), COALESCE(dsm_user,''),
		       COALESCE(info_json,''), COALESCE(status,'unknown'), COALESCE(last_seen_at,0),
		       sort, created_at
		FROM host ORDER BY sort ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Host
	for rows.Next() {
		var h Host
		var infoStr string
		if err := rows.Scan(
			&h.ID, &h.Name, &h.Kind, &h.Transport, &h.Address, &h.Port,
			&h.SSHUser, &h.SSHAuth, &h.SSHHostKey, &h.TransportMode,
			&h.DSMURL, &h.DSMUser, &infoStr, &h.Status, &h.LastSeenAt,
			&h.Sort, &h.CreatedAt,
		); err != nil {
			return nil, err
		}
		if infoStr != "" {
			var info platform.HostInfo
			if json.Unmarshal([]byte(infoStr), &info) == nil {
				h.Info = &info
			}
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Get 获取单台主机。
func (r *Registry) Get(ctx context.Context, id string) (*Host, error) {
	list, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == id {
			return &list[i], nil
		}
	}
	return nil, fmt.Errorf("设备不存在: %s", id)
}

// Create 新增主机并在后台探测状态。
func (r *Registry) Create(ctx context.Context, in CreateOrUpdateInput) (*Host, error) {
	if in.Name == "" {
		return nil, errors.New("请填写设备名称")
	}
	if in.Kind == "" {
		in.Kind = "docker"
	}
	if in.Transport == "" {
		in.Transport = "local"
	}
	if in.Transport == "ssh" {
		if in.Address == "" || in.SSHUser == "" {
			return nil, errors.New("SSH 设备需填写主机地址与用户名")
		}
		if in.Port <= 0 {
			in.Port = 22
		}
		if in.SSHAuth == "" {
			in.SSHAuth = "password"
		}
	}

	id := newID("h_")
	var sshSecretRef, dsmSecretRef string
	var err error
	if in.SSHSecret != "" {
		blob, _ := json.Marshal(sshSecretPayload{Secret: in.SSHSecret, Passphrase: in.SSHPassphrase})
		sshSecretRef, err = r.saveSecret(ctx, string(blob))
		if err != nil {
			return nil, err
		}
	}
	if in.DSMSecret != "" {
		dsmSecretRef, err = r.saveSecret(ctx, in.DSMSecret)
		if err != nil {
			return nil, err
		}
	}

	now := time.Now().Unix()
	_, err = r.st.DB.ExecContext(ctx, `
		INSERT INTO host(
			id, name, kind, transport, address, port, ssh_user, ssh_auth,
			ssh_secret_ref, ssh_host_key, dsm_url, dsm_user, dsm_secret_ref,
			status, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'unknown', ?)`,
		id, in.Name, in.Kind, in.Transport, in.Address, in.Port, in.SSHUser, in.SSHAuth,
		sshSecretRef, in.SSHHostKey, in.DSMURL, in.DSMUser, dsmSecretRef, now,
	)
	if err != nil {
		return nil, err
	}
	// 立即尝试一次探测
	_, _ = r.ProbeAndSave(ctx, id)
	return r.Get(ctx, id)
}

// Update 更新主机信息；若密码/私钥留空则沿用已保存凭证。
func (r *Registry) Update(ctx context.Context, id string, in CreateOrUpdateInput) (*Host, error) {
	cur, err := r.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.Name == "" {
		in.Name = cur.Name
	}
	if in.Kind == "" {
		in.Kind = cur.Kind
	}
	if in.Transport == "" {
		in.Transport = cur.Transport
	}
	if in.Port <= 0 {
		in.Port = cur.Port
		if in.Port <= 0 {
			in.Port = 22
		}
	}
	if in.SSHAuth == "" {
		in.SSHAuth = cur.SSHAuth
	}
	hostKey := in.SSHHostKey
	if hostKey == "" {
		hostKey = cur.SSHHostKey
	}

	_, err = r.st.DB.ExecContext(ctx, `
		UPDATE host SET name=?, kind=?, transport=?, address=?, port=?, ssh_user=?, ssh_auth=?,
		                ssh_host_key=?, dsm_url=?, dsm_user=?
		WHERE id=?`,
		in.Name, in.Kind, in.Transport, in.Address, in.Port, in.SSHUser, in.SSHAuth,
		hostKey, in.DSMURL, in.DSMUser, id,
	)
	if err != nil {
		return nil, err
	}
	if in.SSHSecret != "" {
		blob, _ := json.Marshal(sshSecretPayload{Secret: in.SSHSecret, Passphrase: in.SSHPassphrase})
		ref, err := r.saveSecret(ctx, string(blob))
		if err != nil {
			return nil, err
		}
		_, _ = r.st.DB.ExecContext(ctx, `UPDATE host SET ssh_secret_ref=? WHERE id=?`, ref, id)
	}
	if in.DSMSecret != "" {
		ref, err := r.saveSecret(ctx, in.DSMSecret)
		if err != nil {
			return nil, err
		}
		_, _ = r.st.DB.ExecContext(ctx, `UPDATE host SET dsm_secret_ref=? WHERE id=?`, ref, id)
	}
	_, _ = r.ProbeAndSave(ctx, id)
	return r.Get(ctx, id)
}

// Delete 删除主机（级联删除其下的 app / service）。
func (r *Registry) Delete(ctx context.Context, id string) error {
	_, err := r.st.DB.ExecContext(ctx, `DELETE FROM host WHERE id=?`, id)
	return err
}

// TrustHostKey 将用户确认过的 SSH 指纹写入主机记录。
func (r *Registry) TrustHostKey(ctx context.Context, id, fingerprint string) error {
	if fingerprint == "" {
		return errors.New("指纹不能为空")
	}
	_, err := r.st.DB.ExecContext(ctx, `UPDATE host SET ssh_host_key=? WHERE id=?`, fingerprint, id)
	return err
}

// TestConnection 执行分步连接诊断，不会保存主机。
func (r *Registry) TestConnection(ctx context.Context, in CreateOrUpdateInput) (*TestResult, error) {
	res := &TestResult{}
	if in.Transport == "local" || in.Transport == "" {
		sock := transport.DefaultLocalSocket()
		if _, err := os.Stat(sock); err != nil {
			res.Steps = append(res.Steps, StepResult{
				Step:    "检查本机 Docker Socket",
				Status:  "error",
				Message: fmt.Sprintf("未找到 %s：%v", sock, err),
			})
			return res, nil
		}
		res.Steps = append(res.Steps, StepResult{
			Step:    "检查本机 Docker Socket",
			Status:  "ok",
			Message: sock,
		})
		tr := transport.NewLocal(sock)
		defer tr.Close()
		ad := docker.New("test", tr)
		info, err := ad.Probe(ctx)
		if err != nil {
			res.Steps = append(res.Steps, StepResult{
				Step:    "读取 Docker Engine 信息",
				Status:  "error",
				Message: err.Error(),
			})
			return res, nil
		}
		res.Steps = append(res.Steps, StepResult{
			Step:    "读取 Docker Engine 信息",
			Status:  "ok",
			Message: fmt.Sprintf("Docker %s (%s/%s)", info.DockerVersion, info.OS, info.Arch),
		})
		res.OK = true
		res.HostKeyTrusted = true
		res.TransportMode = string(transport.ModeLocal)
		res.Info = info
		return res, nil
	}

	// SSH 连接测试
	port := in.Port
	if port <= 0 {
		port = 22
	}
	sshTr, err := transport.DialSSH(ctx, transport.SSHConfig{
		Address:        in.Address,
		Port:           port,
		User:           in.SSHUser,
		AuthType:       in.SSHAuth,
		Secret:         in.SSHSecret,
		Passphrase:     in.SSHPassphrase,
		TrustedHostKey: in.SSHHostKey,
		AllowTOFU:      true,
	})
	if err != nil {
		var mismatch *transport.HostKeyMismatchError
		if errors.As(err, &mismatch) {
			res.PresentedHostKey = mismatch.Presented
		}
		res.Steps = append(res.Steps, StepResult{
			Step:    "SSH 连接与 Docker 通道探测",
			Status:  "error",
			Message: err.Error(),
		})
		return res, nil
	}
	defer sshTr.Close()

	res.PresentedHostKey = sshTr.PresentedHostKey()
	res.HostKeyTrusted = in.SSHHostKey != "" && in.SSHHostKey == sshTr.PresentedHostKey()
	res.TransportMode = string(sshTr.Mode())
	res.Steps = append(res.Steps,
		StepResult{
			Step:    "SSH 握手与认证",
			Status:  "ok",
			Message: fmt.Sprintf("主机指纹 %s", sshTr.PresentedHostKey()),
		},
		StepResult{
			Step:    "Docker 通道探测",
			Status:  "ok",
			Message: fmt.Sprintf("使用方式：%s", describeMode(sshTr.Mode())),
		},
	)

	ad := docker.New("test", sshTr)
	info, err := ad.Probe(ctx)
	if err != nil {
		res.Steps = append(res.Steps, StepResult{
			Step:    "读取 Docker Engine 信息",
			Status:  "error",
			Message: err.Error(),
		})
		return res, nil
	}
	res.Steps = append(res.Steps, StepResult{
		Step:    "读取 Docker Engine 信息",
		Status:  "ok",
		Message: fmt.Sprintf("Docker %s · %s (%s/%s)", info.DockerVersion, info.PlatformVersion, info.OS, info.Arch),
	})
	res.OK = true
	res.Info = info
	return res, nil
}

// OpenAdapter 为指定主机建立传输并返回平台适配器（调用方负责 Close）。
func (r *Registry) OpenAdapter(ctx context.Context, hostID string) (platform.Adapter, *Host, error) {
	var (
		h            Host
		sshSecretRef string
		infoStr      string
	)
	err := r.st.DB.QueryRowContext(ctx, `
		SELECT id, name, kind, transport, COALESCE(address,''), COALESCE(port,22),
		       COALESCE(ssh_user,''), COALESCE(ssh_auth,'password'), COALESCE(ssh_secret_ref,''),
		       COALESCE(ssh_host_key,''), COALESCE(transport_mode,''), COALESCE(info_json,''),
		       COALESCE(status,'unknown'), COALESCE(last_seen_at,0)
		FROM host WHERE id=?`, hostID).Scan(
		&h.ID, &h.Name, &h.Kind, &h.Transport, &h.Address, &h.Port,
		&h.SSHUser, &h.SSHAuth, &sshSecretRef, &h.SSHHostKey, &h.TransportMode,
		&infoStr, &h.Status, &h.LastSeenAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, fmt.Errorf("设备不存在: %s", hostID)
	}
	if err != nil {
		return nil, nil, err
	}
	if infoStr != "" {
		var info platform.HostInfo
		if json.Unmarshal([]byte(infoStr), &info) == nil {
			h.Info = &info
		}
	}

	if h.Transport == "local" {
		tr := transport.NewLocal("")
		return docker.New(h.ID, tr), &h, nil
	}

	var payload sshSecretPayload
	if sshSecretRef != "" {
		raw, err := r.loadSecret(ctx, sshSecretRef)
		if err != nil {
			return nil, &h, err
		}
		if json.Unmarshal([]byte(raw), &payload) != nil {
			payload.Secret = raw
		}
	}
	sshTr, err := transport.DialSSH(ctx, transport.SSHConfig{
		Address:        h.Address,
		Port:           h.Port,
		User:           h.SSHUser,
		AuthType:       h.SSHAuth,
		Secret:         payload.Secret,
		Passphrase:     payload.Passphrase,
		TrustedHostKey: h.SSHHostKey,
		AllowTOFU:      h.SSHHostKey == "",
		PreferredMode:  transport.Mode(h.TransportMode),
	})
	if err != nil {
		// 若首选模式失败，清空 PreferredMode 重新探测一次
		if h.TransportMode != "" {
			sshTr, err = transport.DialSSH(ctx, transport.SSHConfig{
				Address:        h.Address,
				Port:           h.Port,
				User:           h.SSHUser,
				AuthType:       h.SSHAuth,
				Secret:         payload.Secret,
				Passphrase:     payload.Passphrase,
				TrustedHostKey: h.SSHHostKey,
				AllowTOFU:      h.SSHHostKey == "",
			})
		}
		if err != nil {
			r.markStatus(ctx, h.ID, "offline", nil, "")
			return nil, &h, err
		}
	}
	if h.SSHHostKey == "" && sshTr.PresentedHostKey() != "" {
		_ = r.TrustHostKey(ctx, h.ID, sshTr.PresentedHostKey())
	}
	return docker.New(h.ID, sshTr), &h, nil
}

// ProbeAndSave 探测主机并更新数据库中的 status / info_json / transport_mode。
func (r *Registry) ProbeAndSave(ctx context.Context, hostID string) (*platform.HostInfo, error) {
	ad, h, err := r.OpenAdapter(ctx, hostID)
	if err != nil {
		return nil, err
	}
	defer ad.Close()
	info, err := ad.Probe(ctx)
	if err != nil {
		r.markStatus(ctx, hostID, "offline", nil, "")
		return nil, err
	}
	r.markStatus(ctx, hostID, "online", info, h.TransportMode)
	return info, nil
}

func (r *Registry) markStatus(ctx context.Context, id, status string, info *platform.HostInfo, mode string) {
	now := time.Now().Unix()
	if info != nil {
		b, _ := json.Marshal(info)
		_, _ = r.st.DB.ExecContext(ctx, `
			UPDATE host SET status=?, info_json=?, last_seen_at=?,
			                transport_mode= CASE WHEN ?<>'' THEN ? ELSE transport_mode END
			WHERE id=?`, status, string(b), now, mode, mode, id)
		return
	}
	_, _ = r.st.DB.ExecContext(ctx, `UPDATE host SET status=? WHERE id=?`, status, id)
}

type sshSecretPayload struct {
	Secret     string `json:"secret"`
	Passphrase string `json:"passphrase,omitempty"`
}

func (r *Registry) saveSecret(ctx context.Context, plain string) (string, error) {
	nonce, sealed, err := r.box.Seal([]byte(plain))
	if err != nil {
		return "", err
	}
	id := newID("sec_")
	_, err = r.st.DB.ExecContext(ctx, `INSERT INTO secret(id, nonce, cipher) VALUES (?, ?, ?)`, id, nonce, sealed)
	return id, err
}

func (r *Registry) loadSecret(ctx context.Context, id string) (string, error) {
	var nonce, cipher []byte
	if err := r.st.DB.QueryRowContext(ctx, `SELECT nonce, cipher FROM secret WHERE id=?`, id).Scan(&nonce, &cipher); err != nil {
		return "", err
	}
	plain, err := r.box.Open(nonce, cipher)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func newID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func describeMode(m transport.Mode) string {
	switch m {
	case transport.ModeStreamLocal:
		return "SSH StreamLocal 直连 /var/run/docker.sock"
	case transport.ModeDialStdio:
		return "SSH docker system dial-stdio"
	case transport.ModeSudoStdio:
		return "SSH sudo docker system dial-stdio"
	default:
		return string(m)
	}
}
