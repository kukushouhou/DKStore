package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kukushouhou/DKStore/internal/version"
)

const (
	envBootstrapped   = "DKSTORE_BOOTSTRAPPED"
	envBaseExecutable = "DKSTORE_BASE_EXE"
	maxBootAttempts   = 3
)

// OverrideState 记录持久化数据目录下热更新二进制（<DataDir>/bin/dkstore）的状态与熔断保护计数。
type OverrideState struct {
	Version        string `json:"version"`
	SHA256         string `json:"sha256"`
	BaseExeSHA256  string `json:"baseExeSha256,omitempty"`
	BootAttempts   int    `json:"bootAttempts"`
	Healthy        bool   `json:"healthy"`
	RolledBackNote string `json:"rolledBackNote,omitempty"`
	UpdatedAt      int64  `json:"updatedAt"`
}

// OverrideBinaryPath 返回数据目录下的持久化自更新二进制路径。
func OverrideBinaryPath(dataDir string) string {
	name := "dkstore"
	if runtime.GOOS == "windows" {
		name = "dkstore.exe"
	}
	return filepath.Join(dataDir, "bin", name)
}

func overrideStatePath(dataDir string) string {
	return filepath.Join(dataDir, "bin", "state.json")
}

func readOverrideState(dataDir string) (OverrideState, error) {
	var st OverrideState
	b, err := os.ReadFile(overrideStatePath(dataDir))
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, err
	}
	return st, nil
}

func writeOverrideState(dataDir string, st OverrideState) error {
	dir := filepath.Join(dataDir, "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := overrideStatePath(dataDir) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, overrideStatePath(dataDir))
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sameFilePath(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 == nil && err2 == nil {
		return filepath.Clean(ra) == filepath.Clean(rb)
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// MaybeBootstrap 在进程启动初期检查 <DataDir>/bin/dkstore 是否存在有效的新版二进制；
// 若存在且未触发连续启动失败熔断，则调用 execve 原地切换至新版运行（保持 PID 不变）。
func MaybeBootstrap(dataDir string, log *slog.Logger) {
	cleanupStaleOldBinary()
	if runtime.GOOS == "windows" || dataDir == "" {
		return
	}
	if os.Getenv(envBootstrapped) == "1" {
		return
	}

	overrideBin := OverrideBinaryPath(dataDir)
	info, err := os.Stat(overrideBin)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return
	}

	exePath, err := os.Executable()
	if err == nil && exePath != "" && sameFilePath(exePath, overrideBin) {
		return
	}

	st, _ := readOverrideState(dataDir)
	curBaseHash, _ := fileSHA256(exePath)

	// 若外层镜像或系统套件本身已被用户更新（可执行文件哈希变化且外层版本 >= 热更版本），自动归位并清理旧热更文件
	pOuter := version.ParseTag(version.Version)
	pOver := version.ParseTag(st.Version)
	if pOuter.Scheme != version.SchemeUnknown && pOver.Scheme != version.SchemeUnknown {
		cmp := version.Compare(pOuter, pOver)
		outerChanged := st.BaseExeSHA256 != "" && curBaseHash != "" && curBaseHash != st.BaseExeSHA256
		if cmp > 0 || (cmp == 0 && outerChanged) {
			_ = os.Remove(overrideBin)
			_ = os.Remove(overrideStatePath(dataDir))
			if exePath != "" {
				_ = os.Remove(filepath.Join(filepath.Dir(exePath), ".dkstore-override"))
			}
			if log != nil {
				log.Info("检测到外层程序版本已追平或超越热更包，自动切回主程序", "outer", version.Version, "override", st.Version)
			}
			return
		}
	}

	// 连续启动未就绪熔断保护（防止损坏包导致容器/服务无限重启）
	if !st.Healthy && st.BootAttempts >= maxBootAttempts {
		_ = os.Remove(overrideBin)
		st.RolledBackNote = fmt.Sprintf("%s failed to start %d times; rolled back to %s", nonEmpty(st.Version, "update"), st.BootAttempts, version.Version)
		_ = writeOverrideState(dataDir, st)
		if log != nil {
			log.Warn("热更二进制连续启动失败，已自动熔断回退至原程序", "version", st.Version, "attempts", st.BootAttempts)
		}
		return
	}

	// 校验文件哈希与架构头合法性
	if st.SHA256 != "" {
		actualHash, err := fileSHA256(overrideBin)
		if err != nil || !strings.EqualFold(actualHash, st.SHA256) {
			_ = os.Remove(overrideBin)
			st.RolledBackNote = "override binary checksum mismatch; rolled back"
			_ = writeOverrideState(dataDir, st)
			return
		}
	}
	if hdrErr := verifyBinaryFileArch(overrideBin, runtime.GOOS, runtime.GOARCH); hdrErr != nil {
		_ = os.Remove(overrideBin)
		st.RolledBackNote = "override binary architecture invalid; rolled back"
		_ = writeOverrideState(dataDir, st)
		return
	}

	_ = os.Chmod(overrideBin, 0o755)
	st.BootAttempts++
	st.Healthy = false
	st.RolledBackNote = ""
	_ = writeOverrideState(dataDir, st)

	env := withEnv(os.Environ(), envBootstrapped, "1")
	if exePath != "" {
		env = withEnv(env, envBaseExecutable, exePath)
	}
	if err := execReplaceProcess(overrideBin, os.Args, env); err != nil {
		// 如遇数据卷挂载为 noexec（如部分 OMV/Linux 数据盘或 /tmp），自动拷入可执行运行路径后再启动
		if errors.Is(err, os.ErrPermission) {
			if binBytes, rErr := os.ReadFile(overrideBin); rErr == nil {
				if fbPath, _, fbErr := stageExecFallbackCopy(binBytes, exePath); fbErr == nil {
					if execReplaceProcess(fbPath, os.Args, env) == nil {
						return
					}
				}
			}
		}
		_ = os.Remove(overrideBin)
		st.RolledBackNote = fmt.Sprintf("exec override failed (%v); rolled back", err)
		_ = writeOverrideState(dataDir, st)
		if log != nil {
			log.Warn("切换热更二进制失败，继续运行原程序", "err", err)
		}
	}
}

// MarkBootHealthy 在 HTTP 服务成功监听后调用，清零启动失败计数并标记当前热更包健康。
func MarkBootHealthy(dataDir string) {
	if dataDir == "" {
		return
	}
	overrideBin := OverrideBinaryPath(dataDir)
	if _, err := os.Stat(overrideBin); err != nil {
		return
	}
	st, err := readOverrideState(dataDir)
	if err != nil {
		return
	}
	if st.Healthy && st.BootAttempts == 0 {
		return
	}
	st.BootAttempts = 0
	st.Healthy = true
	st.UpdatedAt = time.Now().Unix()
	_ = writeOverrideState(dataDir, st)
}

// ExecReload 在优雅关闭 HTTP 与数据库连接后，原地切换进程映像至新版可执行文件（Linux 保持 PID 不变，Windows 拉起新进程退出）。
func ExecReload(targetBin, dataDir string) error {
	env := os.Environ()
	overrideBin := OverrideBinaryPath(dataDir)
	isFallbackOverride := strings.HasSuffix(filepath.Base(targetBin), ".dkstore-override")
	if isFallbackOverride || (dataDir != "" && sameFilePath(targetBin, overrideBin)) {
		env = withEnv(env, envBootstrapped, "1")
		if os.Getenv(envBaseExecutable) == "" {
			if exe, err := os.Executable(); err == nil && exe != "" &&
				!sameFilePath(exe, overrideBin) && !strings.HasSuffix(filepath.Base(exe), ".dkstore-override") {
				env = withEnv(env, envBaseExecutable, exe)
			}
		}
	} else {
		env = withEnv(env, envBootstrapped, "0")
	}
	return execReplaceProcess(targetBin, os.Args, env)
}

// stageExecFallbackCopy 在数据目录挂载了 noexec 时，将已校验的新版二进制拷入容器/系统可执行目录以供运行。
func stageExecFallbackCopy(binBytes []byte, baseExe string) (string, string, error) {
	var dirs []string
	if baseExe != "" {
		if resolved, err := filepath.EvalSymlinks(baseExe); err == nil && resolved != "" {
			dirs = append(dirs, filepath.Dir(resolved))
		} else {
			dirs = append(dirs, filepath.Dir(baseExe))
		}
	}
	dirs = append(dirs, "/usr/local/bin", "/dev/shm", "/run")

	var lastErr error
	seen := make(map[string]bool, len(dirs))
	for _, d := range dirs {
		d = filepath.Clean(d)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			continue
		}
		dst := filepath.Join(d, ".dkstore-override")
		tmp := fmt.Sprintf("%s.tmp-%d", dst, time.Now().UnixNano())
		if err := os.WriteFile(tmp, binBytes, 0o755); err != nil {
			lastErr = err
			continue
		}
		ver, pErr := probeBinaryVersion(tmp)
		if pErr != nil {
			_ = os.Remove(tmp)
			lastErr = pErr
			continue
		}
		if err := os.Rename(tmp, dst); err != nil {
			_ = os.Remove(tmp)
			lastErr = err
			continue
		}
		return dst, ver, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no executable fallback directory available")
	}
	return "", "", lastErr
}

// RolledBackNote 读取并返回可能存在的自动熔断回退提示。
func RolledBackNote(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	st, err := readOverrideState(dataDir)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(st.RolledBackNote)
}

func withEnv(env []string, key, val string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			out = append(out, prefix+val)
			replaced = true
		} else {
			out = append(out, kv)
		}
	}
	if !replaced {
		out = append(out, prefix+val)
	}
	return out
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) != "" {
		return s
	}
	return fallback
}
