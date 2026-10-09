//go:build unix

package prusaslicer

import (
	"os/exec"
	"syscall"
)

// isolate puts the slicer in its own process group and makes cancellation kill the whole
// group, so helper processes cannot outlive a cancelled job.
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
