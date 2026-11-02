//go:build !linux

package xrayrun

import "os/exec"

func dieWithParent(*exec.Cmd) {}
