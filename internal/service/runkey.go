package service

import (
	"fmt"
	"strings"

	"github.com/theclifmeister/terminatr/internal/plat/autostart"
)

// On Windows start at login is a value in the HKCU Run key, which needs no
// admin rights (docs/SPEC.md §3.1). Windows runs it with the user's full
// environment (PATH included), once per login; `tm server start` starts
// the server detached, without a console window, and does nothing when one
// runs already. The value is named JobLabel(), so each home has its own.
// The command window of the start itself shows for an instant.

// runKey is the file name shown for the Run key's value.
func (c Config) runKey() string { return autostart.KeyPath + `\` + c.JobLabel() }

func (c Config) store() autostart.Store {
	if c.Store != nil {
		return c.Store
	}
	return autostart.User()
}

// runCommand is the Run value: tm.exe server start, under cmd.exe to set
// TERMINATR_HOME when this home is not the default.
func (c Config) runCommand() (string, error) {
	cmd := `"` + c.Bin + `" server start`
	if c.Home == "" {
		return cmd, nil
	}
	if strings.ContainsAny(c.Home, `"%^&|<>!`) || strings.Contains(c.Bin, `"`) {
		return "", fmt.Errorf("TERMINATR_HOME %q has characters a startup command can't carry", c.Home)
	}
	return `cmd.exe /d /s /c "set "TERMINATR_HOME=` + c.Home + `"&& ` + cmd + `"`, nil
}

func (c Config) installRunKey() error {
	cmd, err := c.runCommand()
	if err != nil {
		return err
	}
	return c.store().Set(c.JobLabel(), cmd)
}

func (c Config) uninstallRunKey() error { return c.store().Remove(c.JobLabel()) }
