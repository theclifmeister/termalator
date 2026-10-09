//go:build unix

package main

import (
	"os"
	"os/exec"
)

// echoOff turns the terminal's echo off.
func echoOff() {
	noEcho := exec.Command("stty", "-echo")
	noEcho.Stdin = os.Stdin
	noEcho.Run()
}
