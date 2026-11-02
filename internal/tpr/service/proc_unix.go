//go:build !windows

package service

import (
	"errors"
	"os/exec"
	"syscall"
)

func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func configureChild(_ *exec.Cmd) {}

func stopChild(cmd *exec.Cmd) {
	_ = cmd.Process.Signal(syscall.SIGTERM)
}

// detach makes a process outlive the terminal that started it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
