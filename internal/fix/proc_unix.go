//go:build unix

package fix

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup makes a test command the leader of its own process group, and
// makes cancelling it kill the whole group. "npm test" and similar run the real
// work in child processes; killing only the shell would leave them running and
// holding the output pipe open, so the run would never return.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
