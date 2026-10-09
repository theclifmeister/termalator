//go:build windows

package autostart

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

type user struct{}

const subKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func (user) Set(name, command string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, subKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(name, command)
}

func (user) Get(name string) (string, bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, subKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer k.Close()
	v, _, err := k.GetStringValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	return v, err == nil, err
}

func (user) Remove(name string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, subKey, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}
