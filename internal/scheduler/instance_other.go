//go:build !windows

package scheduler

func acquireInstance(string) (func(), bool, error) { return func() {}, true, nil }
