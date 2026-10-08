//go:build !unix

package server

import (
	"errors"
	"syscall"
)

// Not yet ported: the server runs only on Unix. These keep the package
// building elsewhere.

func IsSessionLeader() bool                  { return false }
func Respawn() error                         { return errors.ErrUnsupported }
func Detach() error                          { return errors.ErrUnsupported }
func newSession() *syscall.SysProcAttr       { return nil }
func kill(pid int, sig syscall.Signal) error { return errors.ErrUnsupported }
func alive(pid int) bool                     { return false }
