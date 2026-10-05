package view

// The actions: what clients ask the server to do to a view (the view.*
// control methods, docs/SPEC.md §3.3). Each changes v in place and says
// whether anything changed; the server bumps Seq and broadcasts.

// Attach shows session id, the view's one pane. project, when set,
// becomes the current project.
func (v *View) Attach(id, project string) bool {
	old := v.Clone()
	v.Mode, v.Focus = ModeLayout, id
	if project != "" {
		v.Current = project
	}
	return !Equal(old, *v)
}

// Dashboard shows the dashboard; the session stays for going back.
func (v *View) Dashboard() bool {
	if v.Mode == ModeDashboard {
		return false
	}
	v.Mode = ModeDashboard
	return true
}

// ShowProject shows project's dashboard: the dashboard, with project
// current and its coordinator's row selected.
func (v *View) ShowProject(project string) bool {
	if project == "" {
		return v.Dashboard()
	}
	old := v.Clone()
	v.Mode, v.Current, v.Selected = ModeDashboard, project, "p:"+project
	return !Equal(old, *v)
}

// Remove forgets session id, which ended: a view showing it goes back to
// the dashboard.
func (v *View) Remove(id string) bool {
	if !v.Has(id) {
		return false
	}
	v.Focus = ""
	v.Normalize()
	return true
}

// Prune forgets the session alive says is gone.
func (v *View) Prune(alive func(id string) bool) bool {
	if v.Focus != "" && !alive(v.Focus) {
		return v.Remove(v.Focus)
	}
	return false
}
