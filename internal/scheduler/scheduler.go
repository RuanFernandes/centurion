package scheduler

import (
	"context"
	"errors"

	"github.com/RuanFernandes/centurion/internal/model"
)

var ErrUnsupportedSchedule = errors.New("this schedule is not supported by the local Windows Task Scheduler adapter")

func Register(schedule model.Schedule, executable string) error {
	return register(schedule, executable)
}

func Unregister(scheduleID string) error {
	return unregister(scheduleID)
}

func AcquireInstance(name string) (release func(), acquired bool, err error) {
	return acquireInstance(name)
}

func StartIPC(ctx context.Context, handler func(scheduleID string)) (func(), error) {
	return startIPC(ctx, handler)
}

func ForwardScheduledRun(scheduleID string) error {
	return forwardScheduledRun(scheduleID)
}
