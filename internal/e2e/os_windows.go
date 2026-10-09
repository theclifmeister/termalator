package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// exe is the file name of a program called name.
func exe(name string) string { return name + ".exe" }

// runDirBase holds the run dirs; %TEMP% is short enough for a socket
// path.
var runDirBase = os.TempDir()

// goldenOS names the testdata/golden subdirectory of golden screens for
// this OS only (WaitGolden).
const goldenOS = "windows"

// rootDir is where sessions and windows start: the system drive's root.
var rootDir = filepath.VolumeName(os.TempDir()) + `\`

// basePath is the PATH after the bin dir: git, the POSIX tools beside
// sh (Git for Windows' usr\bin), then Windows' own.
func basePath() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	dirs := []string{
		filepath.Join(root, "System32"), root,
		filepath.Join(root, "System32", "WindowsPowerShell", "v1.0"),
	}
	if p := shPath(); p != "" {
		dirs = append([]string{filepath.Dir(p)}, dirs...)
		// Git for Windows: <root>\usr\bin\sh.exe, <root>\cmd\git.exe.
		if git := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(p))), "cmd"); fileExists(filepath.Join(git, "git.exe")) {
			dirs = append([]string{git}, dirs...)
		}
	}
	if p, err := exec.LookPath("git"); err == nil {
		dirs = append([]string{filepath.Dir(p)}, dirs...)
	}
	return strings.Join(dirs, string(os.PathListSeparator))
}

// osVars are the variables every tm command and window gets on this OS:
// Windows programs find the home in USERPROFILE and their state under
// APPDATA and LOCALAPPDATA, all moved into the test's home.
func osVars(home string) []string {
	return []string{
		"HOME=" + home,
		"USERPROFILE=" + home,
		"APPDATA=" + filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA=" + filepath.Join(home, "AppData", "Local"),
	}
}

// shPath finds a POSIX sh: $E2E_SH, else sh on PATH, else Git for
// Windows' (scenarios type sh syntax into their shells).
func shPath() string {
	if p := os.Getenv("E2E_SH"); p != "" {
		return p
	}
	if p, err := exec.LookPath("sh"); err == nil {
		return p
	}
	for _, dir := range []string{os.Getenv("ProgramFiles"), os.Getenv("LOCALAPPDATA") + `\Programs`} {
		if p := filepath.Join(dir, "Git", "usr", "bin", "sh.exe"); dir != "" && fileExists(p) {
			return p
		}
	}
	return ""
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// sh is the POSIX shell that shell windows and sessions run; the test
// skips without one.
func sh(t testing.TB) string {
	t.Helper()
	p := shPath()
	if p == "" {
		t.Skip("no POSIX sh: install Git for Windows or set E2E_SH")
	}
	return p
}

// linkExe makes link run the program at target under another name: a
// hard link (symlinks need Developer Mode).
func linkExe(target, link string) error { return os.Link(target, link) }

// WriteScript writes an sh script dir/name that runs as a program called
// name: Windows runs it through dir/name.cmd, which hands it to sh.
func WriteScript(dir, name, body string) error {
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		return err
	}
	shim := "@\"" + shPath() + "\" \"%~dpn0\" %*\r\n"
	return os.WriteFile(p+".cmd", []byte(shim), 0o755)
}
