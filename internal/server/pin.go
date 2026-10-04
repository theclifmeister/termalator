package server

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// pinDir holds the server's own copy of its binary, under the home.
const pinDir = "server-bin"

// pinBinary keeps the binary a server runs reachable for as long as the
// server runs, and returns the path to use for it (docs/SPEC.md §3.6,
// Upgrade).
//
// Attach clients of another build re-exec the server's binary, and agent
// hooks run it. Both break when an upgrade replaces the file: `tm update`
// renames a new binary over it, and `brew upgrade` deletes the old keg.
// So the server links its binary into Home/server-bin/tm-<build> (a hard
// link, or a copy across file systems) and uses that path instead. The
// copy carries the same code signature. Only the lock holder calls this,
// so it removes every other pinned binary: no server uses them any more.
func pinBinary(home, bin, build string) (string, error) {
	if bin == "" {
		return "", fmt.Errorf("no binary")
	}
	dir := filepath.Join(home, pinDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := "tm-" + sanitize(build)
	dst := filepath.Join(dir, name)
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.Name() != name {
				os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	// The build id names the binary's hash: an existing file is this one.
	if fi, err := os.Stat(dst); err == nil && fi.Mode().IsRegular() {
		return dst, nil
	}
	tmp := dst + ".tmp"
	os.Remove(tmp)
	if err := os.Link(bin, tmp); err != nil {
		if err := copyFile(bin, tmp); err != nil {
			os.Remove(tmp)
			return "", err
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return dst, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// sanitize keeps a build id usable as a file name.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '+', r == '_':
			return r
		}
		return '_'
	}, s)
}
