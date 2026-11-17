package transport

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ComposeLifecycleTransport 扩展 ExecutableTransport，支持保持未运行服务停止态的整项目重建及 Compose 项目级启停/重启。
type ComposeLifecycleTransport interface {
	ExecutableTransport
	ExecComposeProjectNoStart(ctx context.Context, workDir string, configFiles, envFiles []string, projectName string, pullImages, removeOrphans bool, startServices []string, onChunk func(string)) (string, error)
	ExecComposeAction(ctx context.Context, workDir string, configFiles, envFiles []string, projectName string, action string, services []string) (string, error)
}

func (l *LocalTransport) runComposeSubcommand(ctx context.Context, workDir string, baseFlags []string, subArgs []string, onChunk func(string)) (string, error) {
	env := append(os.Environ(), "PATH="+os.Getenv("PATH")+":/usr/local/bin:/var/packages/ContainerManager/target/usr/bin:/var/packages/Docker/target/usr/bin")
	newCmd := func(name string, a ...string) *exec.Cmd {
		c := exec.CommandContext(ctx, name, a...)
		if workDir != "" {
			if st, err := os.Stat(workDir); err == nil && st.IsDir() {
				c.Dir = workDir
			}
		}
		c.Env = env
		return c
	}
	full := append(append([]string(nil), baseFlags...), subArgs...)
	name, cmdArgs := "docker", append([]string{"compose"}, full...)
	if newCmd("docker", "compose", "version").Run() != nil {
		if _, lerr := exec.LookPath("docker-compose"); lerr == nil {
			name, cmdArgs = "docker-compose", full
		}
	}
	cmd := newCmd(name, cmdArgs...)
	buf := &syncBuffer{onChunk: onChunk}
	cmd.Stdout = buf
	cmd.Stderr = buf
	err := cmd.Run()
	outStr := strings.TrimSpace(string(buf.Bytes()))
	if err != nil {
		return outStr, fmt.Errorf("%s compose: %w (%s)", name, err, outStr)
	}
	return outStr, nil
}

func (l *LocalTransport) ExecComposeProjectNoStart(
	ctx context.Context,
	workDir string,
	configFiles, envFiles []string,
	projectName string,
	pullImages, removeOrphans bool,
	startServices []string,
	onChunk func(string),
) (string, error) {
	baseFlags := buildComposeBaseFlags(workDir, configFiles, envFiles, projectName)
	var outputs []string
	if pullImages {
		out, err := l.runComposeSubcommand(ctx, workDir, baseFlags, []string{"pull"}, onChunk)
		if err != nil {
			return out, fmt.Errorf("compose pull: %w", err)
		}
		if out != "" {
			outputs = append(outputs, out)
		}
	}
	upArgs := []string{"up", "--no-start"}
	if removeOrphans {
		upArgs = append(upArgs, "--remove-orphans")
	}
	out, err := l.runComposeSubcommand(ctx, workDir, baseFlags, upArgs, onChunk)
	if err != nil {
		return out, err
	}
	if out != "" {
		outputs = append(outputs, out)
	}
	if len(startServices) > 0 {
		startArgs := append([]string{"start", "--"}, startServices...)
		sOut, sErr := l.runComposeSubcommand(ctx, workDir, baseFlags, startArgs, onChunk)
		if sErr != nil {
			return sOut, sErr
		}
		if sOut != "" {
			outputs = append(outputs, sOut)
		}
	}
	return strings.Join(outputs, "\n"), nil
}

func (l *LocalTransport) ExecComposeAction(
	ctx context.Context,
	workDir string,
	configFiles, envFiles []string,
	projectName string,
	action string,
	services []string,
) (string, error) {
	baseFlags := buildComposeBaseFlags(workDir, configFiles, envFiles, projectName)
	subArgs := []string{action}
	if len(services) > 0 {
		subArgs = append(subArgs, "--")
		subArgs = append(subArgs, services...)
	}
	return l.runComposeSubcommand(ctx, workDir, baseFlags, subArgs, nil)
}

func (t *SSHTransport) runComposeSubcommandSSH(ctx context.Context, workDir string, baseFlags []string, subArgs []string, onChunk func(string)) (string, error) {
	if err := t.ensureClient(ctx); err != nil {
		return "", err
	}
	dirForShell := workDir
	args := append(append([]string(nil), baseFlags...), subArgs...)
	out, err := t.runPrivilegedCmdStream(ctx, "sh -c "+shellQuote(composeShellScript(dirForShell, args)), nil, onChunk)
	outStr := strings.TrimSpace(string(out))
	if err != nil {
		return outStr, fmt.Errorf("remote docker compose: %w (%s)", err, outStr)
	}
	return outStr, nil
}

func (t *SSHTransport) ExecComposeProjectNoStart(
	ctx context.Context,
	workDir string,
	configFiles, envFiles []string,
	projectName string,
	pullImages, removeOrphans bool,
	startServices []string,
	onChunk func(string),
) (string, error) {
	baseFlags := buildComposeBaseFlags(workDir, configFiles, envFiles, projectName)
	var outputs []string
	if pullImages {
		out, err := t.runComposeSubcommandSSH(ctx, workDir, baseFlags, []string{"pull"}, onChunk)
		if err != nil {
			return out, fmt.Errorf("compose pull: %w", err)
		}
		if out != "" {
			outputs = append(outputs, out)
		}
	}
	upArgs := []string{"up", "--no-start"}
	if removeOrphans {
		upArgs = append(upArgs, "--remove-orphans")
	}
	out, err := t.runComposeSubcommandSSH(ctx, workDir, baseFlags, upArgs, onChunk)
	if err != nil {
		return out, err
	}
	if out != "" {
		outputs = append(outputs, out)
	}
	if len(startServices) > 0 {
		startArgs := append([]string{"start", "--"}, startServices...)
		sOut, sErr := t.runComposeSubcommandSSH(ctx, workDir, baseFlags, startArgs, onChunk)
		if sErr != nil {
			return sOut, sErr
		}
		if sOut != "" {
			outputs = append(outputs, sOut)
		}
	}
	return strings.Join(outputs, "\n"), nil
}

func (t *SSHTransport) ExecComposeAction(
	ctx context.Context,
	workDir string,
	configFiles, envFiles []string,
	projectName string,
	action string,
	services []string,
) (string, error) {
	baseFlags := buildComposeBaseFlags(workDir, configFiles, envFiles, projectName)
	subArgs := []string{action}
	if len(services) > 0 {
		subArgs = append(subArgs, "--")
		subArgs = append(subArgs, services...)
	}
	return t.runComposeSubcommandSSH(ctx, workDir, baseFlags, subArgs, nil)
}
