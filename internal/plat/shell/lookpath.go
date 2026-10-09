package shell

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// LookPathIn finds the program cmd in the directories of path (a PATH
// value), as a shell would. A cmd with a slash is taken as it is. On
// Windows a cmd without one of the PATHEXT extensions is tried with each
// of them in turn (so "codex" finds codex.cmd, an npm shim), and any file
// counts as runnable; elsewhere the file needs an execute bit.
func LookPathIn(cmd, path string) (string, error) {
	return lookPathIn(runtime.GOOS, os.Getenv("PATHEXT"), os.Stat, cmd, path)
}

// defaultPathExt is what Windows uses when PATHEXT is unset.
const defaultPathExt = ".COM;.EXE;.BAT;.CMD"

func lookPathIn(goos, pathExt string, stat func(string) (fs.FileInfo, error), cmd, path string) (string, error) {
	win := goos == "windows"
	if strings.Contains(cmd, "/") || win && strings.Contains(cmd, `\`) {
		return cmd, nil
	}
	names := []string{cmd}
	if win {
		if pathExt == "" {
			pathExt = defaultPathExt
		}
		var exts []string
		for _, e := range strings.Split(pathExt, ";") {
			if e != "" {
				exts = append(exts, strings.ToLower(e))
			}
		}
		names = nil
		if hasExt(cmd, exts) {
			names = append(names, cmd)
		} else {
			for _, e := range exts {
				names = append(names, cmd+e)
			}
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		for _, n := range names {
			p := filepath.Join(dir, n)
			if fi, err := stat(p); err == nil && !fi.IsDir() && (win || fi.Mode()&0o111 != 0) {
				return p, nil
			}
		}
	}
	return "", errors.New(cmd + " not found on the sessions' PATH")
}

func hasExt(name string, exts []string) bool {
	low := strings.ToLower(name)
	for _, e := range exts {
		if strings.HasSuffix(low, e) {
			return true
		}
	}
	return false
}
