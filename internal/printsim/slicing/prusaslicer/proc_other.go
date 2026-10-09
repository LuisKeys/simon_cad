//go:build !unix

package prusaslicer

import "os/exec"

func isolate(cmd *exec.Cmd) {}
