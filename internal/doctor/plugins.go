package doctor

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// unsafePlugins are Claude Code plugins, by id (plugin@marketplace),
// known to break terminatr's rules when Claude loads them in a thread or
// coordinator session, with the reason. Claude loads the user's enabled
// plugins in every session, tm's included.
var unsafePlugins = map[string]string{
	"worktrees@supermods": "its \"Remove N finished\" runs git worktree remove on every clean worktree with a merged PR or no commits ahead, " +
		"which includes a freshly delegated thread's; removing a thread's worktree needs your OK",
}

// plugin is the part of one `claude plugin list --json` entry doctor reads.
type plugin struct {
	ID             string `json:"id"`
	Enabled        bool   `json:"enabled"`
	ProjectEnabled bool   `json:"projectEnabled"`
}

// Plugins warns about each enabled Claude Code plugin on the known-unsafe
// list. Warnings only, with no fix: the plugins are the user's, who can
// disable one with `claude plugin disable <id>`.
func Plugins(d Deps) []Check {
	const g = "plugins"
	path, err := d.LookPath("claude")
	if err != nil {
		return nil // Agents reports a missing claude
	}
	raw, err := d.Run("", path, "plugin", "list", "--json")
	if err != nil {
		return []Check{{Group: g, Name: "claude plugins", Status: Warn, Detail: "couldn't list: " + firstLine(err.Error())}}
	}
	var list []plugin
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return []Check{{Group: g, Name: "claude plugins", Status: Warn, Detail: "couldn't read claude plugin list --json: " + firstLine(err.Error())}}
	}
	var out []Check
	for _, p := range list {
		why, bad := unsafePlugins[p.ID]
		if !bad || !(p.Enabled || p.ProjectEnabled) {
			continue
		}
		out = append(out, Check{Group: g, Name: p.ID, Status: Warn,
			Detail: fmt.Sprintf("unsafe in terminatr sessions: %s (claude plugin disable %s)", why, p.ID)})
	}
	if len(out) == 0 {
		ids := make([]string, 0, len(unsafePlugins))
		for id := range unsafePlugins {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		out = append(out, Check{Group: g, Name: "claude plugins", Status: OK,
			Detail: "none known to be unsafe enabled (checked " + strings.Join(ids, ", ") + ")"})
	}
	return out
}
