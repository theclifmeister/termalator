//go:build unix || windows

package flock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func lockPath(t *testing.T) string {
	return filepath.Join(t.TempDir(), "x.lock")
}

func TestTryLockExcludesAndReleases(t *testing.T) {
	p := lockPath(t)
	a, err := TryLock(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TryLock(p); !errors.Is(err, ErrLocked) {
		t.Fatalf("second TryLock: %v, want ErrLocked", err)
	}
	if held, err := Held(p); err != nil || !held {
		t.Fatalf("Held = %v, %v; want true", held, err)
	}
	a.Unlock()
	if held, err := Held(p); err != nil || held {
		t.Fatalf("Held after Unlock = %v, %v; want false", held, err)
	}
	b, err := TryLock(p)
	if err != nil {
		t.Fatalf("TryLock after Unlock: %v", err)
	}
	b.Unlock()
}

func TestHeldNeverCreates(t *testing.T) {
	p := lockPath(t)
	if held, err := Held(p); err != nil || held {
		t.Fatalf("Held = %v, %v; want false, nil", held, err)
	}
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Held created the file: %v", err)
	}
}

func TestWaitBlocksUntilFree(t *testing.T) {
	p := lockPath(t)
	a, err := TryLock(p)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan *Lock)
	go func() {
		l, err := Wait(p)
		if err != nil {
			t.Error(err)
		}
		got <- l
	}()
	select {
	case <-got:
		t.Fatal("Wait returned while the lock was held")
	case <-time.After(100 * time.Millisecond):
	}
	a.Unlock()
	select {
	case l := <-got:
		l.Unlock()
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after Unlock")
	}
}

func TestInherited(t *testing.T) {
	p := lockPath(t)
	a, err := TryLock(p)
	if err != nil {
		t.Fatal(err)
	}
	fd, err := a.Inheritable()
	if err != nil {
		t.Fatal(err)
	}
	// Same process, same description: re-locking it succeeds, as it does
	// for the exec'd program.
	l := Inherited(fd, p)
	if l == nil {
		t.Fatal("Inherited returned nil for the lock's own descriptor")
	}
	a.Uninheritable()
	// l now owns the descriptor; releasing it ends a's too.
	defer l.Unlock()

	other := filepath.Join(t.TempDir(), "other.lock")
	os.WriteFile(other, nil, 0o600)
	if Inherited(fd, other) != nil {
		t.Fatal("Inherited adopted a descriptor for a different file")
	}
	if Inherited(2, p) != nil || Inherited(-1, p) != nil {
		t.Fatal("Inherited adopted a standard or invalid descriptor")
	}
}
