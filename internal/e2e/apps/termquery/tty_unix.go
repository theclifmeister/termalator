//go:build unix

package main

import "os"

// openTTY opens the controlling terminal for reading and writing.
func openTTY() (in, out *os.File, err error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	return tty, tty, err
}
