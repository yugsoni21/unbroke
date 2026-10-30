//go:build !unix

package fix

import "os/exec"

func ownProcessGroup(cmd *exec.Cmd) {}
