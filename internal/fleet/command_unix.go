//go:build !windows

package fleet

import "os/exec"

func quietCommand(cmd *exec.Cmd) {}
