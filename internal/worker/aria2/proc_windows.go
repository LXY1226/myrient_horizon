//go:build windows

package aria2

import (
	"os/exec"
	"syscall"
)

func setPlatformAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags:    0x00000010, // CREATE_NEW_CONSOLE
		NoInheritHandles: true,
	}
}
