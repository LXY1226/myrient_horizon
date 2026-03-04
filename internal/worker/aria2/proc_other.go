//go:build !windows

package aria2

import "os/exec"

func setPlatformAttrs(cmd *exec.Cmd) {}
