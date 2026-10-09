//go:build windows

package fsx

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// openSync opens path writable: FlushFileBuffers fails with access denied
// on a read-only handle.
func openSync(path string) (*os.File, error) { return os.OpenFile(path, os.O_RDWR, 0) }

// renameTries and renameWait bound the retry in rename: a virus scanner,
// the indexer or a reader without FILE_SHARE_DELETE holds the target for
// a moment, about 3 s in all.
const (
	renameTries = 40
	renameWait  = 75 * time.Millisecond
)

// rename moves tmp over path, retrying while another process has either
// open without sharing (sharing and lock violations, and the access
// denied a pending delete gives).
func rename(tmp, path string) error {
	var err error
	for i := 0; i < renameTries; i++ {
		if err = os.Rename(tmp, path); err == nil || !retryable(err) {
			return err
		}
		time.Sleep(renameWait)
	}
	return err
}

func retryable(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION) ||
		errors.Is(err, windows.ERROR_ACCESS_DENIED)
}

// isLink reports whether fi is a reparse point: a symlink, a junction or
// another mount point (Go reports the latter as irregular).
func isLink(fi fs.FileInfo) bool {
	if fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
		return true
	}
	if a, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok {
		return a.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
	}
	return false
}

// checkOwnerMode fails unless the directory's owner is this user. The
// mode bits mean nothing on Windows: the ACL (inherited from the profile
// folder for %LOCALAPPDATA%) is the access control.
func checkOwnerMode(dir string, fi fs.FileInfo) error {
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("%s: owner: %w", dir, err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("%s: owner: %w", dir, err)
	}
	ok, err := ownedByMe(owner)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s is owned by %s, not by this user", dir, owner)
	}
	return nil
}

// ownedByMe reports whether sid is the user's SID, or the token's default
// owner: an elevated process creates files owned by Administrators.
func ownedByMe(sid *windows.SID) (bool, error) {
	tok := windows.GetCurrentProcessToken()
	u, err := tok.GetTokenUser()
	if err != nil {
		return false, err
	}
	if windows.EqualSid(sid, u.User.Sid) {
		return true, nil
	}
	n := uint32(0)
	windows.GetTokenInformation(tok, windows.TokenOwner, nil, 0, &n)
	buf := make([]byte, n)
	if err := windows.GetTokenInformation(tok, windows.TokenOwner, &buf[0], n, &n); err != nil {
		return false, err
	}
	o := (*tokenOwner)(unsafe.Pointer(&buf[0]))
	return windows.EqualSid(sid, o.Owner), nil
}

// LinkRoleFile makes dir/name hold what points an agent at the role file
// target in the same directory. Symlinks need Developer Mode or admin
// rights on Windows, so name is a small file with Claude's import line,
// "@target". An existing file with that line is kept; anything else at
// name is replaced.
func LinkRoleFile(dir, name, target string) error {
	link := filepath.Join(dir, name)
	want := "@" + filepath.ToSlash(target) + "\n"
	if fi, err := os.Lstat(link); err == nil && fi.Mode().IsRegular() {
		if b, err := os.ReadFile(link); err == nil && string(b) == want {
			return nil
		}
	}
	if err := os.Remove(link); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return WriteAtomic(link, []byte(want), 0o644)
}

// TempRoots are the directories that hold temporary files: os.TempDir
// as given (it may be an 8.3 name, C:\Users\RUNNER~1\...) and as the
// long name.
func TempRoots() []string {
	roots := []string{os.TempDir()}
	if p, err := windows.UTF16PtrFromString(roots[0]); err == nil {
		buf := make([]uint16, windows.MAX_LONG_PATH)
		if n, err := windows.GetLongPathName(p, &buf[0], uint32(len(buf))); err == nil && n > 0 && int(n) < len(buf) {
			if long := windows.UTF16ToString(buf[:n]); long != roots[0] {
				roots = append(roots, long)
			}
		}
	}
	return roots
}

// tokenOwner is TOKEN_OWNER, which x/sys lacks.
type tokenOwner struct{ Owner *windows.SID }
