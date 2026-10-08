package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/config"
)

// The Models page of the , popup's General tab (docs/SPEC.md §11.2):
// each agent's models catalog, the models a thread may run with a line
// on when each fits, and the default a launch passes. The agents
// release new models often, so the user adds, changes and removes them
// here, without a tm release; the first change copies the agent's list
// as released (its manifest's [[models]]) into the settings, and r goes
// back to it.

// catalogItem is one line of the page: an agent's (model -1) or one of
// its models.
type catalogItem struct{ cat, model int }

// catalogView lists the agents' catalogs. Changes are saved at once and
// shown before the save is done, as the other settings are.
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

// catalogWords is the Models row's value: each agent's count of models.
func catalogWords(cats []Catalog) string {
	var parts []string
	for _, c := range cats {
		parts = append(parts, fmt.Sprintf("%s %d", c.Agent, len(c.Models())))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func (v *catalogView) items() []catalogItem {
	var out []catalogItem
	for i, c := range v.cats {
		out = append(out, catalogItem{i, -1})
		for j := range c.Models() {
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

// owned is cat's settings with its own models list: the list as
// released copied in when the settings have none yet.
func owned(c Catalog) config.AgentSettings {
	s := c.Settings
	s.Models = slices.Clone(s.Models)
	if !s.HasModels {
		for _, x := range c.Manifest {
			s.Models = append(s.Models, config.AgentModel{Name: x.Name, About: x.About})
		}
		s.HasModels = true
	}
	return s
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
	switch k.String() {
	case "a":
		v.edit(m, it.cat, -1)
	case "enter", "e":
		v.edit(m, it.cat, it.model)
	case "*", "space", " ":
		if it.model >= 0 {
			return v.run(m, func(m *dash) tea.Cmd { return v.setDefault(m, it.cat, it.model) })
		}
	case "d", "delete":
		if it.model >= 0 {
			return v.run(m, func(m *dash) tea.Cmd { return v.remove(m, it.cat, it.model) })
		}
	case "r":
		return v.run(m, func(m *dash) tea.Cmd { return v.reset(m, it.cat) })
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

// save shows next as cat's settings and writes them.
func (v *catalogView) save(m *dash, cat int, next config.AgentSettings, msg string) tea.Cmd {
	v.err = ""
	c := &v.cats[cat]
	c.Settings = next
	v.sel = min(v.sel, max(len(v.items())-1, 0))
	src, name := m.src, c.Agent
	return m.act(func() actionMsg {
		if err := src.SetModels(name, next); err != nil {
			return actionMsg{err: settingsErr(err)}
		}
		return actionMsg{msg: msg}
	})
}

// edit asks for a model's name, then its about line, and saves it:
// model -1 adds one to cat.
func (v *catalogView) edit(m *dash, cat, model int) {
	c := v.cats[cat]
	var old agent.Model
	title := "add a model to " + c.Agent
	if model >= 0 {
		old = c.Models()[model]
		title = "change " + c.Agent + "'s " + old.Name
	}
	m.prompt(title, "The model's name, as "+c.Agent+" takes it (one word):", old.Name, func(name string) tea.Cmd {
		if err := config.CheckModelName(name); err != nil {
			v.err = err.Error()
			return nil
		}
		if name != old.Name && slices.ContainsFunc(v.cats[cat].Models(), func(x agent.Model) bool { return x.Name == name }) {
			v.err = c.Agent + " already lists " + name
			return nil
		}
		m.push(&inputView{title: title, label: "When it fits, in one line (the coordinator reads it to pick):", text: old.About, max: config.MaxModelAbout,
			submit: func(about string) tea.Cmd {
				return v.run(m, func(m *dash) tea.Cmd { return v.put(m, cat, old.Name, agent.Model{Name: name, About: about}) })
			}})
		return nil
	})
}

// put saves x in cat in place of the model named old ("" adds it).
func (v *catalogView) put(m *dash, cat int, old string, x agent.Model) tea.Cmd {
	c := v.cats[cat]
	s := owned(c)
	am := config.AgentModel{Name: x.Name, About: x.About}
	i := slices.IndexFunc(s.Models, func(y config.AgentModel) bool { return y.Name == old })
	msg := fmt.Sprintf("%s offers %s", c.Agent, x.Name)
	if old == "" || i < 0 {
		s.Models = append(s.Models, am)
	} else {
		s.Models[i] = am
		msg = fmt.Sprintf("%s's %s changed", c.Agent, x.Name)
		if old != x.Name && agent.DefaultOf(c.Models()) == old {
			// The default follows its rename.
			s.HasDefault, s.DefaultModel = true, x.Name
		}
	}
	if err := config.CheckCatalog(s.Models); err != nil {
		v.err = err.Error()
		return nil
	}
	return v.save(m, cat, s, msg)
}

func (v *catalogView) setDefault(m *dash, cat, model int) tea.Cmd {
	c := v.cats[cat]
	models := c.Models()
	if model >= len(models) {
		return nil
	}
	s := c.Settings
	s.HasDefault, s.DefaultModel = true, models[model].Name
	msg := fmt.Sprintf("%s threads run %s unless the coordinator picks another", c.Agent, models[model].Name)
	if models[model].Default {
		s.DefaultModel = ""
		msg = fmt.Sprintf("%s threads run the agent's own default unless the coordinator picks a model", c.Agent)
	}
	return v.save(m, cat, s, msg)
}

func (v *catalogView) remove(m *dash, cat, model int) tea.Cmd {
	c := v.cats[cat]
	models := c.Models()
	if model >= len(models) {
		return nil
	}
	name := models[model].Name
	s := owned(c)
	s.Models = slices.DeleteFunc(s.Models, func(x config.AgentModel) bool { return x.Name == name })
	if s.HasDefault && s.DefaultModel == name {
		s.DefaultModel = ""
	}
	return v.save(m, cat, s, fmt.Sprintf("%s no longer offers %s", c.Agent, name))
}

func (v *catalogView) reset(m *dash, cat int) tea.Cmd {
	c := v.cats[cat]
	if !c.Settings.HasModels && !c.Settings.HasDefault {
		m.msg = c.Agent + "'s models are already as released"
		return nil
	}
	return v.save(m, cat, config.AgentSettings{}, c.Agent+"'s models are as released again")
}

// lines draws the page w cells wide, with each line's item index for
// clicks.
func (v *catalogView) lines(w int) (out []string, hits []int) {
	idx := 0
	for i, c := range v.cats {
		if i > 0 {
			out, hits = append(out, ""), append(hits, noHit)
		}
		models := c.Models()
		src := "as released"
		if c.Settings.HasModels || c.Settings.HasDefault {
			src = "your list (r: as released)"
		}
		def := agent.DefaultOf(models)
		if def == "" {
			def = "the agent's own"
		}
		head := c.Agent + "  " + styleFaint.Render("default "+def+" · "+src)
		if idx == v.sel {
			head = styleSel.Render(fit(ansi.Strip(head), w))
		} else {
			head = styleHead.Render(c.Agent) + "  " + styleFaint.Render("default "+def+" · "+src)
		}
		out, hits = append(out, head), append(hits, idx)
		idx++
		nw := 0
		for _, x := range models {
			nw = max(nw, len([]rune(x.Name)))
		}
		for _, x := range models {
			mark := "  "
			if x.Default {
				mark = "* "
			}
			l := "  " + mark + fit(x.Name, nw) + "  " + x.About
			if idx == v.sel {
				out = append(out, styleSel.Render(fit(l, w)))
			} else {
				out = append(out, styleAccent.Render(fit("  "+mark+fit(x.Name, nw), nw+4))+"  "+styleFaint.Render(fit(x.About, max(w-nw-6, 1))))
			}
			hits = append(hits, idx)
			idx++
		}
		if len(models) == 0 {
			out, hits = append(out, styleFaint.Render("    no models: --model is refused, threads run the agent's own default")), append(hits, noHit)
		}
	}
	if len(v.cats) == 0 {
		out, hits = append(out, styleFaint.Render("no agents")), append(hits, noHit)
	}
	return out, hits
}

func (v *catalogView) render(m *dash) string {
	w := m.inner(viewWidth)
	lines := faintLines("Each agent's models: the ones a thread may run, a line on when each fits (the coordinator reads it), and the default (*) a thread runs when the coordinator picks none. Change them when an agent releases new models; Thread models (All projects) limits which the coordinator may pick.", w)
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
	keys := "a add · enter change · * default · d remove · r as released · ↑ ↓ move · esc back"
	return m.popup(box{title: "Models", body: lines, sel: sel, hits: all, keys: keys})
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
		v.edit(m, it.cat, it.model)
	}
	return nil
}
