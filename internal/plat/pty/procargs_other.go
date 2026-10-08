//go:build !darwin && !linux

package pty

import "errors"

func ProcArgs(pid int) ([]string, error) { return nil, errors.ErrUnsupported }

func ParentPID(pid int) (int, error) { return 0, errors.ErrUnsupported }
