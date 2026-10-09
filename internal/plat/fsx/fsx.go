// Package fsx is terminatr's file system policy: atomic replacement of a
// file, private directories, the temp roots, the role-file link, and path
// identity (Canonical, SamePath, Under).
//
// Most of it is portable. The Unix files check a directory's owner and
// list the temp roots; the Windows port adds a retry on sharing
// violations to rename, an owner check by SID, and a role file that is a
// small CLAUDE.md holding "@AGENTS.md" instead of a symlink.
package fsx

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/theclifmeister/terminatr/internal/plat/caps"
)

// WriteAtomic replaces path with data, mode perm (Replace). The caller
// holds the lock, if it needs one.
func WriteAtomic(path string, data []byte, perm fs.FileMode) error {
	return Stream(path, perm, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// Stream replaces path with what write writes, mode perm (Replace). An
// error from write leaves path as it was and is returned as is.
func Stream(path string, perm fs.FileMode, write func(io.Writer) error) error {
	return Replace(path, perm, func(tmp string) error {
		f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if err := write(f); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	})
}

// Replace replaces path with the file create makes at tmp, a fresh hidden
// name in path's directory, so a reader sees the old file or the new one,
// never half of one. The new file gets mode perm (whatever the umask) and
// is synced to disk before the rename. When create or any step fails, tmp
// is removed and path is left as it was.
func Replace(path string, perm fs.FileMode, create func(tmp string) error) error {
	tmp, err := tempName(path)
	if err != nil {
		return err
	}
	err = create(tmp)
	if err == nil {
		err = syncFile(tmp)
	}
	if err == nil {
		err = os.Chmod(tmp, perm)
	}
	if err == nil {
		err = rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// tempName is a fresh name next to path: hidden (a leading dot), so
// listings that skip dot files skip it too.
func tempName(path string) (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp-"+hex.EncodeToString(b)), nil
}

// syncFile flushes path to disk. It opens path the way the platform's
// flush allows (openSync): read-only on Unix, where create may make tmp a
// hard link to a read-only file; writable on Windows, whose FlushFileBuffers
// needs write access.
func syncFile(path string) error {
	f, err := openSync(path)
	if err != nil {
		return err
	}
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// EnsurePrivateDir creates dir (mode 0700) if needed, then CheckPrivate.
func EnsurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return CheckPrivate(dir)
}

// CheckPrivate fails unless dir is a directory, not a symlink, owned by
// this user and closed to group and others. A missing dir fails with an
// error that matches fs.ErrNotExist.
func CheckPrivate(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if isLink(fi) {
		return fmt.Errorf("%s is a link, not a directory", dir)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	return checkOwnerMode(dir, fi)
}

// Canonical is p absolute, cleaned and with its symlinks resolved; the
// part that doesn't exist is kept as written.
func Canonical(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		p = a
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// SamePath reports whether a and b name the same file by path: equal
// after Canonical, ignoring case where the file system does.
func SamePath(a, b string) bool {
	return equal(Canonical(a), Canonical(b))
}

// Under reports whether p is root or inside it, by its name (Rel).
func Under(p, root string) bool {
	_, ok := Rel(root, p)
	return ok
}

// Rel is p relative to root ("." for root itself) when p is root or
// inside it. It compares cleaned names, ignoring case where the file
// system does, and resolves no symlinks: pass Canonical paths for that.
func Rel(root, p string) (string, bool) {
	if root == "" || p == "" {
		return "", false
	}
	root, p = filepath.Clean(root), filepath.Clean(p)
	if equal(p, root) {
		return ".", true
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	if len(p) > len(prefix) && equal(p[:len(prefix)], prefix) {
		return p[len(prefix):], true
	}
	return "", false
}

func equal(a, b string) bool {
	if caps.CaseFold {
		return strings.EqualFold(a, b)
	}
	return a == b
}
