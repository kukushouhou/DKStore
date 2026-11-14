//go:build windows

package selfupdate

import (
	"os"
	"os/exec"
)

func execReplaceProcess(binPath string, args []string, env []string) error {
	var cmdArgs []string
	if len(args) > 1 {
		cmdArgs = args[1:]
	}
	cmd := exec.Command(binPath, cmdArgs...)
	if len(env) > 0 {
		cmd.Env = env
	} else {
		cmd.Env = os.Environ()
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}

func cleanupStaleOldBinary() {
	if exe, err := os.Executable(); err == nil && exe != "" {
		_ = os.Remove(exe + ".old")
	}
}
