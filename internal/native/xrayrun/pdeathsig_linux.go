//go:build linux

package xrayrun

import (
	"os/exec"
	"syscall"
)

// dieWithParent makes the kernel stop Xray if the agent is killed outright
// (SIGKILL), which would otherwise leave an orphan holding the edge's ports.
func dieWithParent(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
