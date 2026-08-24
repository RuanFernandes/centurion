//go:build windows

package scheduler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

const pipeName = `\\.\pipe\Centurion.Scheduler`

func startIPC(ctx context.Context, handler func(scheduleID string)) (func(), error) {
	if handler == nil {
		return func() {}, fmt.Errorf("scheduler IPC handler is required")
	}
	serverContext, cancel := context.WithCancel(ctx)
	go func() {
		for {
			if serverContext.Err() != nil {
				return
			}
			handle, err := createPipe()
			if err != nil {
				return
			}
			connected, err := connectPipe(handle)
			if err == nil && connected {
				readAndHandle(handle, handler)
			}
			_ = windows.CloseHandle(handle)
		}
	}()
	return cancel, nil
}

func forwardScheduledRun(scheduleID string) error {
	scheduleID = strings.TrimSpace(scheduleID)
	if scheduleID == "" {
		return fmt.Errorf("schedule id is required")
	}
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		handle, err := windows.CreateFile(windows.StringToUTF16Ptr(pipeName), windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
		if err == nil {
			data := []byte(scheduleID + "\n")
			var written uint32
			writeErr := windows.WriteFile(handle, data, &written, nil)
			_ = windows.CloseHandle(handle)
			if writeErr == nil && written == uint32(len(data)) {
				return nil
			}
			lastErr = writeErr
		} else {
			lastErr = err
		}
		if attempt < 4 {
			time.Sleep(250 * time.Millisecond)
		}
	}
	return fmt.Errorf("forward scheduled run to the existing instance: %w", lastErr)
}

func createPipe() (windows.Handle, error) {
	name := windows.StringToUTF16Ptr(pipeName)
	return windows.CreateNamedPipe(name, windows.PIPE_ACCESS_INBOUND, windows.PIPE_TYPE_MESSAGE|windows.PIPE_READMODE_MESSAGE|windows.PIPE_WAIT, windows.PIPE_UNLIMITED_INSTANCES, 512, 512, 0, nil)
}

func connectPipe(handle windows.Handle) (bool, error) {
	err := windows.ConnectNamedPipe(handle, nil)
	if err == nil || err == windows.ERROR_PIPE_CONNECTED {
		return true, nil
	}
	return false, err
}

func readAndHandle(handle windows.Handle, handler func(string)) {
	buffer := make([]byte, 512)
	var read uint32
	if err := windows.ReadFile(handle, buffer, &read, nil); err != nil || read == 0 {
		return
	}
	value := strings.TrimSpace(string(buffer[:read]))
	if value != "" {
		handler(value)
	}
}
