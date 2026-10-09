package update

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// CheckEvery is how often the server asks for the latest release.
const CheckEvery = 24 * time.Hour

// cached is the file CachedLatest keeps: the latest tag as last seen
// and when a check was last tried, even if it failed, so an offline
// machine makes one attempt a day, not one per poll.
type cached struct {
	Checked time.Time `json:"checked"`
	Latest  string    `json:"latest,omitempty"`
}

// CachedLatest returns the latest release tag from the cache file at
// path, asking c (and rewriting the file) only when the cache is older
// than CheckEvery. A failed check keeps the old tag and tries again
// after CheckEvery; the error is returned with it.
func CachedLatest(ctx context.Context, c *Client, path string, now time.Time) (string, error) {
	var st cached
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	if !st.Checked.IsZero() && now.Sub(st.Checked) < CheckEvery && !now.Before(st.Checked) {
		return st.Latest, nil
	}
	rel, err := c.Latest(ctx)
	st.Checked = now
	if err == nil {
		st.Latest = rel.Tag
	}
	if b, merr := json.Marshal(st); merr == nil {
		if os.MkdirAll(filepath.Dir(path), 0o700) == nil {
			_ = os.WriteFile(path, b, 0o600)
		}
	}
	return st.Latest, err
}
