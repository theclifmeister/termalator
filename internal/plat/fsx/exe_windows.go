//go:build windows

package fsx

import (
	"fmt"
	"os"
	"time"
)

// moveAside renames path to a fresh "<path>.old-<utc>" and returns that
// name; "" when path doesn't exist.
func moveAside(path string) (string, error) {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return "", nil
	}
	stamp := time.Now().UTC().Format("20060102T150405")
	for n := 0; ; n++ {
		aside := path + asideInfix + stamp
		if n > 0 {
			aside = fmt.Sprintf("%s-%d", aside, n)
		}
		if _, err := os.Lstat(aside); err == nil {
			continue
		}
		return aside, rename(path, aside)
	}
}
