package agent

import "fmt"

// TodoMap says which hook event changes the agent's todo list and how
// (docs/SPEC.md §8.2). Field values are dotted payload paths.
//
//   - op = "replace": the event carries the whole list at List; each item
//     has the fields named by ID (optional), Text, ActiveText and Status.
//   - op = "upsert": the event creates or updates one item; ID, Text,
//     ActiveText and Status are paths in the payload itself. Absent fields
//     keep the stored value; StatusDefault applies to new items.
//   - op = "reset": the event starts a new, empty list.
type TodoMap struct {
	Op            TodoOp            `toml:"op"`
	Event         string            `toml:"event"`
	Match         map[string]string `toml:"match"`
	List          string            `toml:"list"`
	ID            string            `toml:"id"`
	Text          string            `toml:"text"`
	ActiveText    string            `toml:"active_text"`
	Status        string            `toml:"status"`
	StatusDefault TodoStatus        `toml:"status_default"`
	// StatusMap maps the harness's status words to pending, in_progress,
	// completed, or "@remove" (drop the item). Unlisted words must already
	// be one of the three statuses.
	StatusMap map[string]string `toml:"status_map"`
}

const todoRemove = "@remove"

func validTodoStatus(s TodoStatus) bool {
	return s == TodoPending || s == TodoInProgress || s == TodoCompleted
}

func (tm TodoMap) validate() error {
	if tm.Event == "" {
		return fmt.Errorf("needs an event")
	}
	switch tm.Op {
	case TodoReplace:
		if tm.List == "" || tm.Text == "" || tm.Status == "" {
			return fmt.Errorf("op = replace needs list, text and status")
		}
	case TodoUpsert:
		if tm.ID == "" {
			return fmt.Errorf("op = upsert needs id")
		}
	case TodoReset:
	default:
		return fmt.Errorf("op %q is not replace|upsert|reset", tm.Op)
	}
	if tm.StatusDefault != "" && !validTodoStatus(tm.StatusDefault) {
		return fmt.Errorf("status_default %q is not pending|in_progress|completed", tm.StatusDefault)
	}
	for k, v := range tm.StatusMap {
		if v != todoRemove && !validTodoStatus(TodoStatus(v)) {
			return fmt.Errorf("status_map %q -> %q is not pending|in_progress|completed|@remove", k, v)
		}
	}
	return nil
}

// status maps a harness status word. remove is true for "@remove".
func (tm TodoMap) status(raw string) (st TodoStatus, remove bool, err error) {
	if m, ok := tm.StatusMap[raw]; ok {
		if m == todoRemove {
			return "", true, nil
		}
		return TodoStatus(m), false, nil
	}
	if !validTodoStatus(TodoStatus(raw)) {
		return "", false, fmt.Errorf("unknown status %q", raw)
	}
	return TodoStatus(raw), false, nil
}

// change turns a matching event into a TodoChange.
func (tm TodoMap) change(payload map[string]any) (TodoChange, error) {
	switch tm.Op {
	case TodoReset:
		return TodoChange{Op: TodoReset}, nil
	case TodoUpsert:
		id, ok := lookupString(payload, tm.ID)
		if !ok || id == "" {
			return TodoChange{}, fmt.Errorf("no id at %q", tm.ID)
		}
		p := TodoPatch{ID: id, Default: tm.StatusDefault}
		if p.Default == "" {
			p.Default = TodoPending
		}
		if s, ok := lookupString(payload, tm.Text); ok && tm.Text != "" {
			p.Text = &s
		}
		if s, ok := lookupString(payload, tm.ActiveText); ok && tm.ActiveText != "" {
			p.ActiveText = &s
		}
		if raw, ok := lookupString(payload, tm.Status); ok && tm.Status != "" {
			st, remove, err := tm.status(raw)
			if err != nil {
				return TodoChange{}, err
			}
			p.Remove = remove
			if !remove {
				p.Status = &st
			}
		}
		return TodoChange{Op: TodoUpsert, Patch: p}, nil
	}

	raw, ok := lookup(payload, tm.List)
	if !ok {
		return TodoChange{}, fmt.Errorf("no field %q", tm.List)
	}
	items, ok := raw.([]any)
	if !ok {
		return TodoChange{}, fmt.Errorf("%q is not a list", tm.List)
	}
	todos := make([]Todo, 0, len(items))
	for i, it := range items {
		obj, ok := it.(map[string]any)
		if !ok {
			return TodoChange{}, fmt.Errorf("%s[%d] is not an object", tm.List, i)
		}
		rawStatus, _ := lookupString(obj, tm.Status)
		st, remove, err := tm.status(rawStatus)
		if err != nil {
			return TodoChange{}, fmt.Errorf("%s[%d]: %w", tm.List, i, err)
		}
		if remove {
			continue
		}
		t := Todo{Status: st}
		t.Text, _ = lookupString(obj, tm.Text)
		if tm.ID != "" {
			t.ID, _ = lookupString(obj, tm.ID)
		}
		if tm.ActiveText != "" {
			t.ActiveText, _ = lookupString(obj, tm.ActiveText)
		}
		todos = append(todos, t)
	}
	return TodoChange{Op: TodoReplace, Items: todos}, nil
}

// ApplyTodo applies a change to a stored list and returns the new list.
// The input slice is not modified.
func ApplyTodo(list []Todo, ch TodoChange) []Todo {
	switch ch.Op {
	case TodoReset:
		return []Todo{}
	case TodoReplace:
		return append([]Todo{}, ch.Items...)
	}
	p := ch.Patch
	out := make([]Todo, 0, len(list)+1)
	found := false
	for _, t := range list {
		if t.ID != p.ID {
			out = append(out, t)
			continue
		}
		found = true
		if p.Remove {
			continue
		}
		if p.Text != nil {
			t.Text = *p.Text
		}
		if p.ActiveText != nil {
			t.ActiveText = *p.ActiveText
		}
		if p.Status != nil {
			t.Status = *p.Status
		}
		out = append(out, t)
	}
	if !found && !p.Remove {
		t := Todo{ID: p.ID, Status: p.Default}
		if p.Text != nil {
			t.Text = *p.Text
		}
		if p.ActiveText != nil {
			t.ActiveText = *p.ActiveText
		}
		if p.Status != nil {
			t.Status = *p.Status
		}
		out = append(out, t)
	}
	return out
}
