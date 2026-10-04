package main

import (
	"os"
	"runtime"

	"golang.org/x/term"
)

func termSize() (int, int) {
	c, r, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return 120, 40
	}
	return c, r
}

// maxRSSBytes normalises getrusage ru_maxrss: bytes on macOS, KiB on Linux.
func maxRSSBytes(v int64) int64 {
	if runtime.GOOS == "linux" {
		return v << 10
	}
	return v
}
