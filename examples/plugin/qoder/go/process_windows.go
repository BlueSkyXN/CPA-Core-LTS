//go:build windows

package main

import "os/exec"

func configureRunnerProcess(_ *exec.Cmd) {}

func terminateRunnerProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
