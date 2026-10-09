//go:build !windows

package fsx

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// LinkRoleFile makes dir/name point at the role file target in the same
// directory, so an agent that reads name reads target: a relative
// symlink. An existing link to target is kept; anything else at name is
// replaced.
func LinkRoleFile(dir, name, target string) error {
	link := filepath.Join(dir, name)
	if t, err := os.Readlink(link); err == nil && t == target {
		return nil
	}
	if err := os.Remove(link); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Symlink(target, link)
}
