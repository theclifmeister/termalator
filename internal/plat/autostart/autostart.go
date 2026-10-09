// Package autostart is the per-user list of programs Windows starts at
// login: the HKCU Run key. It needs no admin rights, and Task Manager's
// Startup tab lists and switches its entries. (Other systems start
// programs at login through their service managers, internal/service.)
package autostart

import "errors"

// KeyPath is the registry key whose values Windows runs at login.
const KeyPath = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`

// ErrUnsupported means this system has no such list.
var ErrUnsupported = errors.New("no per-user startup list on this system")

// Store is the startup list; tests replace it with their own.
type Store interface {
	// Set makes login run command, a command line, under name.
	Set(name, command string) error
	// Get returns the command line stored under name; ok is false when
	// there is none.
	Get(name string) (command string, ok bool, err error)
	// Remove deletes name; removing what isn't there is fine.
	Remove(name string) error
}

// User is the real list.
func User() Store { return user{} }
