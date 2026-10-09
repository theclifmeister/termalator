//go:build unix

package e2e

import (
	"os"
	"path/filepath"
	"testing"
)

// exe is the file name of a program called name.
func exe(name string) string { return name }

// runDirBase holds the run dirs: macOS temp dirs are too long for a
// socket path; /tmp is short.
const runDirBase = "/tmp"

// goldenOS names the testdata/golden subdirectory of golden screens for
// this OS only: none, the shared ones are Unix's.
const goldenOS = ""

// rootDir is where sessions and windows start.
const rootDir = "/"

// basePath is the PATH after the bin dir.
func basePath() string { return "/usr/bin:/bin:/usr/sbin:/sbin" }

// osVars are the variables every tm command and window gets on this OS.
func osVars(home string) []string { return []string{"HOME=" + home} }

// sh is the POSIX shell that shell windows and sessions run.
func sh(t testing.TB) string { return "/bin/sh" }

// shSession is how a shell session names sh: the dashboard shows it.
func shSession(t testing.TB) string { return "/bin/sh" }

// linkExe makes link run the program at target under another name.
func linkExe(target, link string) error { return os.Symlink(target, link) }

// WriteScript writes an sh script dir/name that runs as a program called
// name.
func WriteScript(dir, name, body string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755)
}
