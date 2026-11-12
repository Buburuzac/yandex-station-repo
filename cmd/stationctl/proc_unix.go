//go:build linux || darwin

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// sig0 — «нулевой» сигнал для проверки, жив ли процесс (Signal(0)).
func sig0() os.Signal { return syscall.Signal(0) }

// setPgid запускает процесс в своей группе, чтобы убить всё дерево разом.
func setPgid(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree гасит всю группу процесса (сам процесс + его потомки, держащие pipe).
func killTree(p *os.Process) {
	if p == nil {
		return
	}
	if pgid, err := syscall.Getpgid(p.Pid); err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return
	}
	_ = p.Kill()
}
