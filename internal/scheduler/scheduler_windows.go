//go:build windows

package scheduler

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/RuanFernandes/centurion/internal/model"
)

func register(schedule model.Schedule, executable string) error {
	if schedule.ID == "" || schedule.Cron == "" || executable == "" {
		return fmt.Errorf("schedule id, cron and executable are required")
	}
	name := taskName(schedule.ID)

	args := []string{"/Create", "/TN", name, "/TR", fmt.Sprintf(`"%s" --scheduled-run %s`, executable, schedule.ID), "/F"}
	fields := strings.Fields(schedule.Cron)
	if len(fields) != 5 {
		return ErrUnsupportedSchedule
	}
	if strings.HasPrefix(fields[0], "*/") && fields[1] == "*" && fields[2] == "*" && fields[3] == "*" && fields[4] == "*" {
		minutes, err := strconv.Atoi(strings.TrimPrefix(fields[0], "*/"))
		if err != nil || minutes <= 0 || minutes > 1440 {
			return ErrUnsupportedSchedule
		}
		args = append(args, "/SC", "MINUTE", "/MO", strconv.Itoa(minutes))
	} else if fields[0] != "*" && fields[1] != "*" && fields[2] == "*" && fields[3] == "*" && fields[4] == "*" {
		hour, hourErr := strconv.Atoi(fields[1])
		minute, minuteErr := strconv.Atoi(fields[0])
		if hourErr != nil || minuteErr != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
			return ErrUnsupportedSchedule
		}
		args = append(args, "/SC", "DAILY", "/ST", fmt.Sprintf("%02d:%02d", hour, minute))
	} else {
		return ErrUnsupportedSchedule
	}
	if output, err := exec.Command("schtasks.exe", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("register Windows Task Scheduler task: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func unregister(scheduleID string) error {
	if strings.TrimSpace(scheduleID) == "" {
		return nil
	}
	output, err := exec.Command("schtasks.exe", "/Delete", "/TN", taskName(scheduleID), "/F").CombinedOutput()
	if err != nil && !strings.Contains(strings.ToLower(string(output)), "cannot find") && !strings.Contains(strings.ToLower(string(output)), "does not exist") {
		return fmt.Errorf("unregister Windows Task Scheduler task: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func taskName(scheduleID string) string {
	return filepath.Join("\\Centurion", scheduleID)
}
