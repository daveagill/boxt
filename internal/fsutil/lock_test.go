package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWriteFileExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := WriteFileExclusive(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileExclusive(path, []byte("two"), 0o600); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second write = %v, want os.ErrExist", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "one" {
		t.Fatalf("content = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestWithLockSerializes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "same")
	var active, maxActive int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := WithLock(path, func() error {
				cur := atomic.AddInt32(&active, 1)
				for {
					old := atomic.LoadInt32(&maxActive)
					if cur <= old || atomic.CompareAndSwapInt32(&maxActive, old, cur) {
						break
					}
				}
				time.Sleep(time.Millisecond)
				atomic.AddInt32(&active, -1)
				return nil
			}); err != nil {
				t.Errorf("WithLock: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&maxActive); got > 1 {
		t.Fatalf("max concurrent lock holders = %d", got)
	}
}
