// Package open opens a web address in the user's browser.
package open

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// URL starts the platform's opener for url and returns without waiting.
func URL(url string) error {
	name, args := command(runtime.GOOS, url, wsl(), func(n string) bool {
		_, err := exec.LookPath(n)
		return err == nil
	})
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// wsl reports whether this is Linux under Windows Subsystem for Linux.
func wsl() bool {
	if os.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	return err == nil && strings.Contains(strings.ToLower(string(b)), "microsoft")
}

// command picks the opener for goos; have says whether a program is on
// PATH. Under WSL it is wslview, else explorer.exe. On Windows it is
// rundll32: explorer exits 1 and cmd's start splits the address at &.
func command(goos, url string, inWSL bool, have func(string) bool) (string, []string) {
	switch {
	case goos == "darwin":
		return "open", []string{url}
	case goos == "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	case inWSL && have("wslview"):
		return "wslview", []string{url}
	case inWSL:
		return "explorer.exe", []string{url}
	}
	return "xdg-open", []string{url}
}
