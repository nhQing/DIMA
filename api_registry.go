package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// registryRepo is what one repository of a project holds on the registry.
type registryRepo struct {
	Repo      string        `json:"repo"`
	Rows      []RegistryRow `json:"rows"`
	Unmatched []RemoteTag   `json:"unmatched"`
	Error     string        `json:"error,omitempty"`
}

// readRegistry lists, per repository a project pushes to, the tags found on
// the registry and the environment each belongs to. One repository failing
// — no login, a typo in the image name — does not hide the others.
func (a *api) readRegistry(p Project) []registryRepo {
	history := a.store.Builds()
	out := []registryRepo{}
	for _, repo := range projectRepos(p) {
		r := registryRepo{Repo: repo, Rows: []RegistryRow{}, Unmatched: []RemoteTag{}}
		tags, err := a.reg().ListTags(repo)
		if err != nil {
			r.Error = err.Error()
		} else {
			r.Rows, r.Unmatched = matchTags(p, repo, tags, history)
		}
		out = append(out, r)
	}
	return out
}

func (a *api) registryTags(w http.ResponseWriter, r *http.Request) {
	p, ok := a.store.Config().FindProject(r.URL.Query().Get("projectId"))
	if !ok {
		fail(w, http.StatusNotFound, "không tìm thấy dự án")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": a.readRegistry(p)})
}

// registryImport records chosen registry tags in the history.
//
// The request names tags, not digests: the registry is asked again here, so
// the digest written into the history — the thing promote will re-tag — is
// the registry's answer and never something the page sent back.
func (a *api) registryImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID string `json:"projectId"`
		// Keys are "envId|tag".
		Keys []string `json:"keys"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "JSON không hợp lệ")
		return
	}
	p, ok := a.store.Config().FindProject(req.ProjectID)
	if !ok {
		fail(w, http.StatusNotFound, "không tìm thấy dự án")
		return
	}
	wanted := map[string]bool{}
	for _, k := range req.Keys {
		wanted[k] = true
	}
	if len(wanted) == 0 {
		fail(w, http.StatusBadRequest, "chưa chọn tag nào")
		return
	}

	// A registry without push times (the generic API) leaves the order of
	// its tags as the only clue. Stamping those "now" would make the oldest
	// imported tag the newest record — and the newest success is what every
	// screen takes as the environment's current version. They are dated just
	// before everything the environment already has instead, one second
	// apart, keeping the lister's newest-first order.
	history := a.store.Builds()
	undated := map[string]time.Time{}
	nextUndated := func(envID string) time.Time {
		t, ok := undated[envID]
		if !ok {
			t = time.Now()
			for _, b := range history {
				if b.EnvID == envID && b.StartedAt.Before(t) {
					t = b.StartedAt
				}
			}
			t = t.Add(-time.Minute)
		}
		undated[envID] = t.Add(-time.Second)
		return t
	}

	added := 0
	problems := []string{}
	for _, repo := range a.readRegistry(p) {
		if repo.Error != "" {
			problems = append(problems, repo.Repo+": "+repo.Error)
			continue
		}
		for _, row := range repo.Rows {
			if !wanted[row.EnvID+"|"+row.Tag] || row.Known {
				continue
			}
			when := row.PushedAt
			if when.IsZero() {
				when = nextUndated(row.EnvID)
			}
			a.store.addBuild(Build{
				ID:          newID(),
				ProjectID:   p.ID,
				ProjectName: p.Name,
				EnvID:       row.EnvID,
				EnvName:     row.EnvName,
				Version:     row.Version,
				Tag:         row.Tag,
				Ref:         row.Ref,
				Digest:      row.Digest,
				Kind:        KindRegistry,
				Status:      "success",
				// It is on the registry: that is where it was read from.
				Pushed:     true,
				Command:    "đọc từ registry " + repo.Repo + " — không phải do DIMA build trên máy này",
				StartedAt:  when,
				FinishedAt: when,
			})
			added++
		}
	}
	if added == 0 && len(problems) > 0 {
		fail(w, http.StatusBadGateway, strings.Join(problems, "; "))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": added, "problems": problems})
}
