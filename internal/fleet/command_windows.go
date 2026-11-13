package fleet

import (
	"os/exec"
	"syscall"
)

func quietCommand(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }
