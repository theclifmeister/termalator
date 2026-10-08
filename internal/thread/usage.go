package thread

import "fmt"

// Usage is the tokens and dollars a thread's turns used, summed over its
// life, as the agent's mod reported them (docs/SPEC.md §8.6, Mods).
// Numbers only: no text from a turn ever lands here. CostUSD is what the
// agent's own ledger said, so it is as exact as the agent's price table.
type Usage struct {
	Turns         int     `toml:"turns" json:"turns"`
	Input         int64   `toml:"input" json:"input"`
	Output        int64   `toml:"output" json:"output"`
	CacheRead     int64   `toml:"cache_read" json:"cache_read"`
	CacheCreation int64   `toml:"cache_creation" json:"cache_creation"`
	CostUSD       float64 `toml:"cost_usd" json:"cost_usd"`
	// PlanPct is how much of the agent's plan limit (a subscription's
	// window, e.g. Codex on ChatGPT) was used when last reported: the
	// latest, not a sum. HasPlan says one was reported; such an agent
	// has no dollar cost, so none is shown.
	PlanPct float64 `toml:"plan_pct,omitempty" json:"plan_pct,omitempty"`
	HasPlan bool    `toml:"has_plan,omitempty" json:"has_plan,omitempty"`
}

// Add is u and o summed.
func (u Usage) Add(o Usage) Usage {
	r := Usage{u.Turns + o.Turns, u.Input + o.Input, u.Output + o.Output,
		u.CacheRead + o.CacheRead, u.CacheCreation + o.CacheCreation, u.CostUSD + o.CostUSD, u.PlanPct, u.HasPlan}
	if o.HasPlan {
		r.PlanPct, r.HasPlan = o.PlanPct, true
	}
	return r
}

// money is the cost part of the short form: "$3.41", or the plan limit
// ("plan 12%") for an agent that reports one instead of a cost.
func (u Usage) money() string {
	if u.HasPlan {
		return fmt.Sprintf("plan %.0f%%", u.PlanPct)
	}
	return fmt.Sprintf("$%.2f", u.CostUSD)
}

// Tokens is every token counted: read from the cache or not.
func (u Usage) Tokens() int64 { return u.Input + u.Output + u.CacheRead + u.CacheCreation }

// Zero reports whether nothing was used.
func (u Usage) Zero() bool { return u == Usage{} }

// String is the short form: "1.2M tokens, $3.41" or "1.2M tokens, plan 12%"; "" for none.
func (u Usage) String() string {
	if u.Zero() {
		return ""
	}
	return fmt.Sprintf("%s tokens, %s", Compact(u.Tokens()), u.money())
}

// Detail is String with the split: "1.2M tokens (12k in, 240k out, 900k
// cache read, 48k cache write), $3.41, 14 turns"; "" for none.
func (u Usage) Detail() string {
	if u.Zero() {
		return ""
	}
	return fmt.Sprintf("%s tokens (%s in, %s out, %s cache read, %s cache write), %s, %d turn%s",
		Compact(u.Tokens()), Compact(u.Input), Compact(u.Output), Compact(u.CacheRead), Compact(u.CacheCreation),
		u.money(), u.Turns, map[bool]string{true: "s"}[u.Turns != 1])
}

// Compact writes n as 940, 12.3k or 1.2M.
func Compact(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

// TaskUsage is the usage of the threads that worked on each task, by
// task number, and Total the usage of all of them: the resolved ones
// too, since what they used was spent.
func TaskUsage(recs []*Record) (byTask map[int]Usage, total Usage) {
	byTask = map[int]Usage{}
	for _, r := range recs {
		total = total.Add(r.Usage)
		if id := r.TaskID(); id > 0 && !r.Usage.Zero() {
			byTask[id] = byTask[id].Add(r.Usage)
		}
	}
	return byTask, total
}
