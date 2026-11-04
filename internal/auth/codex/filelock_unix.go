//go:build !windows

package codex

import (
	"context"
	"fmt"
	"os"
	"syscall"
)

func acquireInterProcessLockFile(ctx context.Context, authFilePath string) (func(), error) {
	lockPath := canonicalLockPath(authFilePath) + ".lock"
	f, errOpen := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if errOpen != nil {
		return nil, fmt.Errorf("failed to open credential lock file %s: %w", lockPath, errOpen)
	}
	if errLock := waitForFileLock(ctx, func() (bool, error) {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == syscall.EWOULDBLOCK || err == syscall.EINTR {
			return false, nil
		}
		return err == nil, err
	}); errLock != nil {
		_ = f.Close()
		return nil, fmt.Errorf("failed to acquire flock on %s: %w", lockPath, errLock)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
