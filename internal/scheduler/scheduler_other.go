//go:build !windows

package scheduler

import (
	"errors"

	"github.com/RuanFernandes/centurion/internal/model"
)

func register(model.Schedule, string) error {
	return errors.New("Windows Task Scheduler is only available on Windows")
}
func unregister(string) error { return nil }
