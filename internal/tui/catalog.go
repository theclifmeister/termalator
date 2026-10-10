package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/terminatr/internal/config"
	"github.com/theclifmeister/terminatr/internal/models"
)

// The Models page of the , popup's General tab (docs/SPEC.md §8.2,
// §11.2): the models each installed agent offers, as the agent itself
// said (tm ships no list), with the user's settings over them: the
// default (*) a thread runs when the coordinator picks none, models
// hidden, models added that the agent accepts but doesn't list, and the
// ones the user's account refused. R asks the agent again.

// catalogItem is one line of the page: an agent's (model -1) or one of
// its models (catalogRows' index).
type catalogItem struct{ cat, model int }

// catalogView lists the installed agents' models. Changes are saved at
// once and shown before the save is done, as the other settings are.
type catalogView struct {
	cats []Catalog
	sel  int
	err  string
}

// openCatalog opens the page on m.catalogs (read when the , popup
// opened), which its changes update, so the Models row follows them.
func (m *dash) openCatalog() {
	if m.catalogs == nil {
		m.catalogs = m.src.Catalogs()
	}
	m.push(&catalogView{cats: m.catalogs})
}

// catalogWords is the Models row's value: each agent's count of
// models, or that they are unknown.
func catalogWords(cats []Catalog) string {
	var parts []string
	for _, c := range cats {
		if c.Known {
			parts = append(parts, fmt.Sprintf("%s %d", c.Agent, len(c.Models)))
		} else {
			parts = append(parts, c.Agent+" unknown")
		}
	}
	if len(parts) == 0 {
		return "no agent installed"
	}
	return strings.Join(parts, ", ")
}

// catalogRows are an agent's model lines: the available models, then
// the hidden and the refused ones.
func catalogRows(c Catalog) []models.Model {
	out := slices.Clone(c.Models)
	out = append(out, c.Hidden...)
	return append(out, c.Refused...)
}

func (v *catalogView) items() []catalogItem {
	var out []catalogItem
	for i, c := range v.cats {
		out = append(out, catalogItem{i, -1})
		for j := range catalogRows(c) {
			out = append(out, catalogItem{i, j})
		}
	}
	return out
}

func (v *catalogView) current() (catalogItem, bool) {
	items := v.items()
	if v.sel < 0 || v.sel >= len(items) {
		return catalogItem{}, false
	}
	return items[v.sel], true
}

// model is the item's model, if it is one.
func (v *catalogView) model(it catalogItem) (models.Model, bool) {
	if it.model < 0 {
		return models.Model{}, false
	}
	rows := catalogRows(v.cats[it.cat])
	if it.model >= len(rows) {
		return models.Model{}, false
	}
	return rows[it.model], true
}

// next is cat's settings to change: the older models list's names kept
// as added ones, since a save writes the new form.
func next(c Catalog) config.AgentSettings {
	s := c.Settings
	return config.AgentSettings{Hide: slices.Clone(s.Hide), Add: s.Added(), DefaultModel: s.DefaultModel}
}

func (v *catalogView) key(m *dash, k tea.KeyPressMsg) tea.Cmd {
	it, ok := v.current()
	switch k.String() {
	case "esc", "q":
		m.pop()
		return nil
	case "up", "k", "down", "j", "pgup", "pgdown":
		v.sel = moveSel(v.sel, scrollKeys[k.String()], len(v.items()))
		return nil
	}
	if !ok {
		return nil
	}
	c := v.cats[it.cat]
	x, isModel := v.model(it)
	switch k.String() {
	case "R":
		return v.refresh(m, it.cat)
	case "r":
		return v.run(m, func(m *dash) tea.Cmd { return v.reset(m, it.cat) })
	case "a":
		if !c.Known {
			v.err = c.Agent + "'s models are unknown (" + c.Reason + "): nothing to add to"
			return nil
		}
		v.edit(m, it.cat, "")
	case "enter", "e":
		switch {
		case !isModel:
			if c.Known {
				v.edit(m, it.cat, "")
			} else {
				return v.refresh(m, it.cat)
			}
		case x.Yours:
			v.edit(m, it.cat, x.Name)
		default:
			m.msg = x.Name + " is " + c.Agent + "'s own: * makes it the default, h hides it"
		}
	case "*", "space", " ":
		if isModel {
			return v.run(m, func(m *dash) tea.Cmd { return v.setDefault(m, it.cat, x) })
		}
	case "h":
		if isModel {
			return v.run(m, func(m *dash) tea.Cmd { return v.hide(m, it.cat, x) })
		}
	case "d", "delete":
		if isModel {
			if !x.Yours {
				m.msg = c.Agent + " lists " + x.Name + ": h hides it"
				return nil
			}
			return v.run(m, func(m *dash) tea.Cmd { return v.remove(m, it.cat, x.Name) })
		}
	case "u":
		if isModel && x.Refusal != nil {
			return v.unrefuse(m, it.cat, x.Name)
		}
	}
	return nil
}

// run runs a change now, or after the save in flight, as the settings
// lists do.
func (v *catalogView) run(m *dash, fn func(m *dash) tea.Cmd) tea.Cmd {
	if m.busy {
		m.queued = append(m.queued, fn)
		return nil
	}
	return fn(m)
}

// save shows s as cat's settings and writes them.
func (v *catalogView) save(m *dash, cat int, s config.AgentSettings, msg string) tea.Cmd {
	v.err = ""
	c := &v.cats[cat]
	*c = c.With(s)
	v.sel = min(v.sel, max(len(v.items())-1, 0))
	src, name := m.src, c.Agent
	return m.act(func() actionMsg {
		if err := src.SetModels(name, s); err != nil {
			return actionMsg{err: settingsErr(err)}
		}
		return actionMsg{msg: msg}
	})
}

// edit asks for a model's name and adds it to cat, or renames old, one
// the user added.
func (v *catalogView) edit(m *dash, cat int, old string) {
	c := v.cats[cat]
	title := "add a model to " + c.Agent
	if old != "" {
		title = "rename " + old
	}
	m.prompt(title, "A model "+c.Agent+" takes but doesn't list (one word, as "+c.Agent+"'s --model takes it):", old, func(name string) tea.Cmd {
		if err := config.CheckModelName(name); err != nil {
			v.err = err.Error()
			return nil
		}
		if _, ok := v.cats[cat].Find(name); ok && name != old {
			v.err = c.Agent + " already offers " + name
			return nil
		}
		return v.run(m, func(m *dash) tea.Cmd {
			c := v.cats[cat]
			s := next(c)
			if i := slices.Index(s.Add, old); old != "" && i >= 0 {
				s.Add[i] = name
				if s.DefaultModel == old {
					s.DefaultModel = name // the default follows its rename
				}
			} else {
				s.Add = append(s.Add, name)
			}
			return v.save(m, cat, s, fmt.Sprintf("%s offers %s, which you added", c.Agent, name))
		})
	})
}

func (v *catalogView) setDefault(m *dash, cat int, x models.Model) tea.Cmd {
	c := v.cats[cat]
	if !c.Has(x.Name) {
		v.err = x.Name + " isn't offered now: it can't be the default"
		return nil
	}
	s := next(c)
	if s.DefaultModel == x.Name {
		s.DefaultModel = ""
		return v.save(m, cat, s, fmt.Sprintf("%s threads run the agent's own default unless the coordinator picks a model", c.Agent))
	}
	s.DefaultModel = x.Name
	return v.save(m, cat, s, fmt.Sprintf("%s threads run %s unless the coordinator picks another", c.Agent, x.Name))
}

func (v *catalogView) hide(m *dash, cat int, x models.Model) tea.Cmd {
	c := v.cats[cat]
	if x.Yours {
		m.msg = "you added " + x.Name + ": d removes it"
		return nil
	}
	s := next(c)
	if i := slices.Index(s.Hide, x.Name); i >= 0 {
		s.Hide = slices.Delete(s.Hide, i, i+1)
		return v.save(m, cat, s, fmt.Sprintf("%s offers %s again", c.Agent, x.Name))
	}
	s.Hide = append(s.Hide, x.Name)
	if s.DefaultModel == x.Name {
		s.DefaultModel = ""
	}
	return v.save(m, cat, s, fmt.Sprintf("%s's %s is hidden: the coordinator can't pick it", c.Agent, x.Name))
}

func (v *catalogView) remove(m *dash, cat int, name string) tea.Cmd {
	c := v.cats[cat]
	s := next(c)
	s.Add = slices.DeleteFunc(s.Add, func(n string) bool { return n == name })
	if s.DefaultModel == name {
		s.DefaultModel = ""
	}
	return v.save(m, cat, s, fmt.Sprintf("%s no longer offers %s", c.Agent, name))
}

func (v *catalogView) reset(m *dash, cat int) tea.Cmd {
	c := v.cats[cat]
	if c.Settings.Empty() {
		m.msg = c.Agent + "'s models are already as " + c.Agent + " lists them"
		return nil
	}
	return v.save(m, cat, config.AgentSettings{}, c.Agent+"'s models are as "+c.Agent+" lists them again")
}

func (v *catalogView) unrefuse(m *dash, cat int, name string) tea.Cmd {
	c := v.cats[cat]
	src := m.src
	return m.act(func() actionMsg {
		if err := src.Unrefuse(c.Agent, name); err != nil {
			return actionMsg{err: settingsErr(err)}
		}
		return actionMsg{msg: fmt.Sprintf("%s is offered again: the next refusal marks it again", name), catalogs: true}
	})
}

// refresh asks the agent for its models again, in the background.
func (v *catalogView) refresh(m *dash, cat int) tea.Cmd {
	c := v.cats[cat]
	src := m.src
	m.msg = "asking " + c.Agent + " for its models…"
	return m.act(func() actionMsg {
		if err := src.RefreshModels(c.Agent); err != nil {
			return actionMsg{err: fmt.Errorf("%s: %w", c.Agent, err), catalogs: true}
		}
		return actionMsg{msg: c.Agent + " answered", catalogs: true}
	})
}

// reload reads the catalogs again after a refresh or an unrefuse.
func (v *catalogView) reload(m *dash) {
	m.catalogs = m.src.Catalogs()
	v.cats = m.catalogs
	v.sel = min(v.sel, max(len(v.items())-1, 0))
}

// lines draws the page w cells wide, with each line's item index for
// clicks.
func (v *catalogView) lines(w int) (out []string, hits []int) {
	idx := 0
	now := time.Now()
	for i, c := range v.cats {
		if i > 0 {
			out, hits = append(out, ""), append(hits, noHit)
		}
		info := c.Source(now)
		if c.Known {
			def := c.Settings.DefaultModel
			if def == "" || !c.Has(def) {
				def = "the agent's own"
			}
			info = fmt.Sprintf("%d models · %s · default %s", len(c.Models), info, def)
		}
		if idx == v.sel {
			out = append(out, styleSel.Render(fit(c.Agent+"  "+info, w)))
		} else {
			out = append(out, styleHead.Render(c.Agent)+"  "+styleFaint.Render(fit(info, max(w-len(c.Agent)-2, 1))))
		}
		hits = append(hits, idx)
		idx++
		rows := catalogRows(c)
		nw := 0
		for _, x := range rows {
			nw = max(nw, len([]rune(x.Name)))
		}
		for _, x := range rows {
			mark, note := "  ", x.About()
			switch {
			case x.Refusal != nil:
				mark, note = "! ", "refused for your account: "+x.Refusal.Reason+" (u: offer it again)"
			case slices.Contains(c.Settings.Hide, x.Name):
				mark, note = "- ", "hidden (h: show)"
			case x.Name == c.Settings.DefaultModel:
				mark = "* "
			}
			if x.Yours {
				note = strings.TrimPrefix(note+" · yours, "+c.Agent+" doesn't list it", " · ")
			}
			if x.Older && x.Refusal == nil {
				note = strings.TrimPrefix(note+" · older", " · ")
			}
			l := "  " + mark + fit(x.Name, nw) + "  " + note
			if idx == v.sel {
				out = append(out, styleSel.Render(fit(l, w)))
			} else {
				name := styleAccent.Render(fit("  "+mark+fit(x.Name, nw), nw+4))
				if x.Refusal != nil || mark == "- " {
					name = styleFaint.Render(fit("  "+mark+fit(x.Name, nw), nw+4))
				}
				out = append(out, name+"  "+styleFaint.Render(fit(note, max(w-nw-6, 1))))
			}
			hits = append(hits, idx)
			idx++
		}
		if !c.Known {
			out, hits = append(out, styleFaint.Render(fit("    threads run "+c.Agent+"'s own default; --model is refused (R asks again)", w))), append(hits, noHit)
		} else if len(rows) == 0 {
			out, hits = append(out, styleFaint.Render("    "+c.Agent+" lists no models")), append(hits, noHit)
		}
	}
	if len(v.cats) == 0 {
		out, hits = append(out, styleWarn.Render("no agent installed: tm can't run coordinators or threads (tm doctor)")), append(hits, noHit)
	}
	return out, hits
}

func (v *catalogView) render(m *dash) string {
	w := m.inner(viewWidth)
	lines := faintLines("The models each installed agent offers you, as the agent itself lists them for your login (tm ships no list; R asks again). * is the default a thread runs when the coordinator picks none; hide a model to keep it from the coordinator, or add one the agent takes but doesn't list. Thread models (All projects, or a project's own) limits them further.", w)
	head := len(lines) + 1
	lines = append(lines, "")
	body, hits := v.lines(w)
	lines = append(lines, body...)
	all := make([]int, head, head+len(hits))
	for i := range all {
		all[i] = noHit
	}
	all = append(all, hits...)
	if v.err != "" {
		lines = append(lines, "", styleBad.Render(v.err))
	}
	sel := -1
	if i := slices.Index(hits, v.sel); i >= 0 {
		sel = head + i
	}
	keys := "* default · h hide · a add · d remove · u unrefuse · r reset · R ask again · esc back"
	return m.popup(box{title: "Models", body: lines, sel: sel, hits: all, keys: ansi.Truncate(keys, w, "…")})
}

func (v *catalogView) click(m *dash, item, _ int, _ bool) tea.Cmd {
	if item == noHit {
		return nil
	}
	if item != v.sel {
		v.sel = item
		return nil
	}
	if it, ok := v.current(); ok {
		if x, isModel := v.model(it); isModel {
			return v.run(m, func(m *dash) tea.Cmd { return v.setDefault(m, it.cat, x) })
		}
	}
	return nil
}
