package doctor

import (
	"fmt"
	"strings"

	"github.com/theclifmeister/termilator/internal/config"
)

// Settings checks config.toml for values tm no longer has: a project
// whose complete_tasks is the removed "released" is read as "by you"
// until the user picks again in its Settings.
func Settings(d Deps) []Check {
	c, err := config.Load()
	if err != nil {
		return []Check{{Group: "settings", Name: "config.toml", Status: Warn, Detail: firstLine(err.Error())}}
	}
	slugs := c.Removed()
	if len(slugs) == 0 {
		return nil
	}
	return []Check{{Group: "settings", Name: "complete tasks", Status: Warn,
		Detail: fmt.Sprintf("\"when released\" was removed, so tasks complete by you in %s: pick again in Settings (by you or when merged)", strings.Join(slugs, ", "))}}
}
