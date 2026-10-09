//go:build windows

package fsx

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// A file another process holds open without FILE_SHARE_DELETE blocks the
// rename; it goes through once the holder lets go.
func TestRenameRetriesSharingViolation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, _ := windows.UTF16PtrFromString(path)
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(400 * time.Millisecond)
		windows.CloseHandle(h)
	}()
	if err := WriteAtomic(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "new" {
		t.Fatalf("file reads %q", b)
	}
}

func TestWriteAtomicReadOnlyTargetHandle(t *testing.T) {
	// The sync bug: every WriteAtomic failed with access denied.
	if err := WriteAtomic(filepath.Join(t.TempDir(), "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestJunctionIsLink(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "real")
	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "junction")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, dir).CombinedOutput(); err != nil {
		t.Skipf("mklink /J: %v: %s", err, out)
	}
	if err := CheckPrivate(link); err == nil {
		t.Fatal("a junction passed")
	}
}

func TestOwnedByMe(t *testing.T) {
	tok := windows.GetCurrentProcessToken()
	u, err := tok.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := ownedByMe(u.User.Sid); err != nil || !ok {
		t.Fatalf("own SID: %v %v", ok, err)
	}
	other, _ := windows.StringToSid("S-1-5-32-546") // BUILTIN\Guests
	if ok, _ := ownedByMe(other); ok {
		t.Fatal("Guests accepted")
	}
}
