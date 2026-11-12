//go:build !linux && !darwin

package main

import (
	"os"
	"os/exec"
)

func sig0() os.Signal { return os.Interrupt } // не-Unix: заглушка (проверка живости груба)

// Фолбэк для не-Unix хостов сборки: без групп процессов.
func setPgid(cmd *exec.Cmd) {}

func killTree(p *os.Process) {
	if p != nil {
		_ = p.Kill()
	}
}
