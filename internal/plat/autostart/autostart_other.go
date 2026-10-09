//go:build !windows

package autostart

type user struct{}

func (user) Set(name, command string) error   { return ErrUnsupported }
func (user) Get(string) (string, bool, error) { return "", false, ErrUnsupported }
func (user) Remove(name string) error         { return ErrUnsupported }
