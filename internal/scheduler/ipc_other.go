//go:build !windows

package scheduler

import (
	"context"
	"errors"
)

func startIPC(context.Context, func(string)) (func(), error) { return func() {}, nil }
func forwardScheduledRun(string) error {
	return errors.New("scheduler IPC is only available on Windows")
}
