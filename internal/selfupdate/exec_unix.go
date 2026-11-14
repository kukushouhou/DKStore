//go:build !windows

package selfupdate

import (
	"os"
	"syscall"
)

func execReplaceProcess(binPath string, args []string, env []string) error {
	argv := []string{binPath}
	if len(args) > 1 {
		argv = append(argv, args[1:]...)
	}
	if len(env) == 0 {
		env = os.Environ()
	}
	return syscall.Exec(binPath, argv, env)
}

func cleanupStaleOldBinary() {}
