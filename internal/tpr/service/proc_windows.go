//go:build windows

package service

import (
	"os/exec"
	"syscall"
)

const (
	createNewProcessGroup = 0x00000200
	createNoWindow        = 0x08000000
	ctrlBreakEvent        = 1
	stillActive           = 259
	processQueryLimited   = 0x1000
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	generateConsoleCtrlEvent = kernel32.NewProc("GenerateConsoleCtrlEvent")
)

func pidAlive(pid int) bool {
	handle, err := syscall.OpenProcess(processQueryLimited, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = syscall.CloseHandle(handle) }()
	var code uint32
	if err := syscall.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	return code == stillActive
}

// configureChild gives the child its own process group, which is what lets the
// supervisor send it CTRL_BREAK alone. Go treats that as an interrupt, so the
// server shuts down cleanly. The child must share the supervisor's console for the
// event to reach it, so it must not get CREATE_NO_WINDOW, which would give it a
// console of its own. The supervisor already has no visible window when detached.
func configureChild(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

func stopChild(cmd *exec.Cmd) {
	result, _, _ := generateConsoleCtrlEvent.Call(uintptr(ctrlBreakEvent), uintptr(cmd.Process.Pid))
	if result == 0 {
		// There is no shared console to deliver the event through, so a clean exit
		// is not possible. End the process directly.
		_ = cmd.Process.Kill()
	}
}

// detach makes a process outlive the terminal that started it, with no window.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | createNoWindow}
}
