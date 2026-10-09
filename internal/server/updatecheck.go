package server

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/theclifmeister/terminatr/internal/config"
	"github.com/theclifmeister/terminatr/internal/update"
	"github.com/theclifmeister/terminatr/internal/version"
)

// latestRelease is a release newer than this server.
type latestRelease struct{ Tag, Upgrade string }

// consultEvery is how often the server looks at the update cache; the
// cache itself asks GitHub at most once a day (update.CheckEvery).
const consultEvery = time.Hour

// checkForUpdate refreshes s.latest in the background, never blocking
// the caller (session.list, the dashboard's poll). It does nothing for
// a source build, with [ui] update_check off, or with
// TERMINATR_UPDATE_URL=off; offline, the check fails quietly and the
// last known tag stays (docs/SPEC.md §10.1).
func (s *Server) checkForUpdate() {
	now := time.Now()
	last := s.checkedAt.Load()
	if version.Channel == "" || (last != 0 && now.Sub(time.Unix(last, 0)) < consultEvery) {
		return
	}
	if !s.checkedAt.CompareAndSwap(last, now.Unix()) {
		return
	}
	go func() {
		if os.Getenv(update.EnvAPI) == "off" {
			return
		}
		if cfg, err := config.Load(); err == nil && !cfg.UpdateCheck {
			s.latest.Store(nil)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		path := filepath.Join(s.opts.Paths.Home, "state", "update.json")
		tag, err := update.CachedLatest(ctx, update.NewClient(os.Getenv), path, now)
		if err != nil {
			s.log.Printf("update check: %v", err)
		}
		if newer, ok := update.Newer(tag, version.Version); !ok || !newer {
			s.latest.Store(nil)
			return
		}
		exe, _ := os.Executable()
		in := update.Detect(exe, version.Channel)
		up := in.Upgrade
		if up == "" {
			up = "tm update"
		}
		s.latest.Store(&latestRelease{Tag: tag, Upgrade: up})
	}()
}
