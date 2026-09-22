package lockfile

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLockCreatesParentDirAndFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "x.lock")
	unlock, err := Lock(path)
	if err != nil {
		t.Fatalf("Lock() returned error: %v", err)
	}
	unlock()
}

func TestTryLockFailsWhenAlreadyHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")

	unlock, err := Lock(path)
	if err != nil {
		t.Fatalf("Lock() returned error: %v", err)
	}
	defer unlock()

	_, ok, err := TryLock(path)
	if err != nil {
		t.Fatalf("TryLock() returned error: %v", err)
	}
	if ok {
		t.Error("TryLock() succeeded while another holder had the lock, want ok=false")
	}
}

func TestTryLockSucceedsWhenFree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")

	unlock, ok, err := TryLock(path)
	if err != nil {
		t.Fatalf("TryLock() returned error: %v", err)
	}
	if !ok {
		t.Fatal("TryLock() failed on a free lock, want ok=true")
	}
	unlock()

	// Released: a second TryLock must succeed too.
	unlock2, ok2, err := TryLock(path)
	if err != nil || !ok2 {
		t.Fatalf("TryLock() after unlock: ok=%v err=%v, want ok=true", ok2, err)
	}
	unlock2()
}

// TestLockSerializesGoroutines proves Lock actually provides mutual
// exclusion across concurrent callers within one process, the property
// hook_classify.go's per-session lock and flush.go's flush lock both
// depend on: a counter incremented under the lock, read back, and
// decremented must never observe more than one goroutine "inside" at once.
func TestLockSerializesGoroutines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")

	var inside int32
	var maxObserved int32
	var wg sync.WaitGroup
	const n = 20
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock, err := Lock(path)
			if err != nil {
				t.Errorf("Lock() returned error: %v", err)
				return
			}
			defer unlock()

			cur := atomic.AddInt32(&inside, 1)
			for {
				m := atomic.LoadInt32(&maxObserved)
				if cur <= m || atomic.CompareAndSwapInt32(&maxObserved, m, cur) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			atomic.AddInt32(&inside, -1)
		}()
	}
	wg.Wait()

	if maxObserved != 1 {
		t.Errorf("max concurrent holders = %d, want 1 (Lock did not serialize)", maxObserved)
	}
}
