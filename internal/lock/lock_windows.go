package lock

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

var lockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

func tryLock(f *os.File) error {
	const (
		failImmediately = 1
		exclusiveLock   = 2
		lockViolation   = syscall.Errno(33)
	)
	var offset syscall.Overlapped
	result, _, err := lockFileEx.Call(f.Fd(), failImmediately|exclusiveLock, 0, 1, 0, uintptr(unsafe.Pointer(&offset)))
	if result != 0 {
		return nil
	}
	if errors.Is(err, lockViolation) {
		return ErrBusy
	}
	return err
}
