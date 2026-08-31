//go:build !windows

package main

import (
	"errors"
	"os/exec"
	"syscall"
)

func configureRunnerProcess(cmd *exec.Cmd) {
	if cmd != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
}

func terminateRunnerProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return nil
	}
	errKill := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(errKill, syscall.ESRCH) {
		return nil
	}
	return errKill
}
