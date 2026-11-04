package codex

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestCodexRefreshOwnershipCancellation(t *testing.T) {
	for _, native := range []bool{false, true} {
		name := "process-mutex"
		if native {
			name = "native-lock"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "credential.json")
			var release func()
			if native {
				var err error
				release, err = acquireInterProcessLockFile(context.Background(), path)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				mu := lockForFilePath(path)
				mu.Lock()
				release = mu.Unlock
			}
			defer release()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered := make(chan struct{})
			result := make(chan error, 1)
			go func() {
				close(entered)
				result <- WithFileLockContext(ctx, path, func() error { return errors.New("entered another owner's critical section") })
			}()
			<-entered
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled ownership wait returned %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("canceled caller remained trapped behind credential ownership")
			}
		})
	}
}
