//go:build !windows

package codex

import "os/exec"

func configureHiddenProcess(_ *exec.Cmd) {}
