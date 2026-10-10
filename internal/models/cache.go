package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/home"
	"github.com/theclifmeister/terminatr/internal/plat/fsx"
)

// The statuses of an agent's answer.
const (
	StatusOK        = "ok"         // it listed the models of a logged-in user
	StatusLoggedOut = "logged_out" // no one is logged in to it
	StatusFailed    = "failed"     // it couldn't be asked (Reason says why)
)

// Cache is what the agent last answered, in <home>/state/models/<agent>.json.
type Cache struct {
	Agent   string    `json:"agent"`
	Version string    `json:"version,omitempty"`
	Asked   time.Time `json:"asked"`
	Status  string    `json:"status"`
	Reason  string    `json:"reason,omitempty"`
	// Note is said beside a kept answer: a later probe timed out.
	Note        string              `json:"note,omitempty"`
	Account     string              `json:"account,omitempty"`
	Fingerprint string              `json:"fingerprint,omitempty"`
	Models      []agent.ListedModel `json:"models,omitempty"`
	// Refused are the models the user's account refused in a session,
	// by name: forgotten when the account or the agent's version changes,
	// or when the user unmarks one.
	Refused map[string]Refusal `json:"refused,omitempty"`
}

// Refusal is one model the account refused.
type Refusal struct {
	Reason      string    `json:"reason"`
	At          time.Time `json:"at"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	Version     string    `json:"version,omitempty"`
}

// Dir is <home>/state/models.
func Dir() (string, error) {
	h, err := home.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "state", "models"), nil
}

var mu sync.Mutex // this process's writes; each write is atomic

// Load reads the agent's cache; ok false when it was never asked.
func Load(name string) (Cache, bool) {
	dir, err := Dir()
	if err != nil {
		return Cache{}, false
	}
	data, err := os.ReadFile(filepath.Join(dir, name+".json"))
	if err != nil {
		return Cache{}, false
	}
	var c Cache
	if json.Unmarshal(data, &c) != nil || c.Agent != name {
		return Cache{}, false
	}
	return c, true
}

func save(c Cache) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteAtomic(filepath.Join(dir, c.Agent+".json"), append(data, '\n'), 0o600)
}

// ErrNotInstalled is an agent whose command isn't on PATH.
var ErrNotInstalled = errors.New("not installed")

// ErrNoLister is an agent whose manifest can't ask it for its models.
var ErrNoLister = errors.New("no list_models in its manifest")

// Refresh asks the installed agent m for its models and saves the
// answer. env is the environment sessions get (nil: tm's own); its
// PATH finds the agent. A probe that times out keeps the last answer
// while the agent's version is the same (Note says so); any other
// failure leaves none, since it may be another login's. Refusals
// carry over only while the account and the version are the same.
func Refresh(ctx context.Context, m *agent.Manifest, env []string) (Cache, error) {
	if env == nil {
		env = os.Environ()
	}
	path, err := LookPath(m, env)
	if err != nil {
		return Cache{}, ErrNotInstalled
	}
	if m.ListModels == nil {
		return Cache{}, ErrNoLister
	}
	version := Version(ctx, path, m, env)
	listing, perr := Probe(ctx, path, m.ListModels, env)
	mu.Lock()
	defer mu.Unlock()
	old, had := Load(m.Name)
	now := time.Now()
	if perr != nil {
		if errors.Is(perr, ErrTimeout) && had && old.Status == StatusOK && old.Version == version {
			old.Note = fmt.Sprintf("last asked %s; then it didn't answer in time", old.Asked.Local().Format("2006-01-02"))
			return old, save(old)
		}
		c := Cache{Agent: m.Name, Version: version, Asked: now, Status: StatusFailed, Reason: perr.Error()}
		return c, errors.Join(perr, save(c))
	}
	c := Cache{Agent: m.Name, Version: version, Asked: now, Status: StatusOK, Account: listing.Account, Fingerprint: listing.Fingerprint}
	if listing.LoggedOut {
		c.Status = StatusLoggedOut
	} else {
		c.Models = listing.Models
	}
	for name, r := range old.Refused {
		if r.Fingerprint == c.Fingerprint && r.Version == c.Version {
			if c.Refused == nil {
				c.Refused = map[string]Refusal{}
			}
			c.Refused[name] = r
		}
	}
	return c, save(c)
}

// MarkRefused records that the account refused the agent's model, with
// the agent's message, against the account and version last asked.
func MarkRefused(name, model, reason string) error {
	mu.Lock()
	defer mu.Unlock()
	c, ok := Load(name)
	if !ok {
		c = Cache{Agent: name, Status: StatusFailed, Reason: "not asked yet"}
	}
	if c.Refused == nil {
		c.Refused = map[string]Refusal{}
	}
	c.Refused[model] = Refusal{Reason: reason, At: time.Now(), Fingerprint: c.Fingerprint, Version: c.Version}
	return save(c)
}

// Unrefuse forgets that the account refused the agent's model (the user
// enabled it, say on Bedrock).
func Unrefuse(name, model string) error {
	mu.Lock()
	defer mu.Unlock()
	c, ok := Load(name)
	if !ok {
		return nil
	}
	if _, ok := c.Refused[model]; !ok {
		return nil
	}
	delete(c.Refused, model)
	return save(c)
}

// Forget removes the agent's cache, for tests and a reinstall.
func Forget(name string) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(dir, name+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// SaveForTest writes c as its agent's cache (modelstest).
func SaveForTest(c Cache) error {
	mu.Lock()
	defer mu.Unlock()
	return save(c)
}
