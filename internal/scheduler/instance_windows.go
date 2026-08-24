//go:build windows

package scheduler

import (
	"syscall"
	"unsafe"
)

var (
	kernel32     = syscall.NewLazyDLL("kernel32.dll")
	createMutexW = kernel32.NewProc("CreateMutexW")
	closeHandle  = kernel32.NewProc("CloseHandle")
)

func acquireInstance(name string) (func(), bool, error) {
	value, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, false, err
	}
	handle, _, callErr := createMutexW.Call(0, 0, uintptr(unsafe.Pointer(value)))
	if handle == 0 {
		return nil, false, callErr
	}
	if callErr == syscall.Errno(183) {
		_, _, _ = closeHandle.Call(handle)
		return func() {}, false, nil
	}
	return func() { _, _, _ = closeHandle.Call(handle) }, true, nil
}
