package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// Handlers for cleaning up a project's old local images. See cleanup.go.

func (a *api) cleanupPlan(p Project, keep int) ([]CleanupItem, error) {
	images, err := a.store.LocalImages()
	if err != nil {
		return nil, err
	}
	containers, err := readContainers(a.store.Config().DockerBin)
	if err != nil {
		// Without the container list a used image could look removable.
		// docker rmi would still refuse it, but the plan must not claim
		// otherwise, so no list means no plan.
		return nil, err
	}
	return planCleanup(p, images, a.store.Builds(), containers, keep), nil
}

func keepParam(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 {
		return 3
	}
	return n
}

func (a *api) cleanupPreview(w http.ResponseWriter, r *http.Request) {
	p, ok := a.store.Config().FindProject(r.URL.Query().Get("projectId"))
	if !ok {
		fail(w, http.StatusNotFound, "không tìm thấy dự án")
		return
	}
	keep := keepParam(r.URL.Query().Get("keep"))
	items, err := a.cleanupPlan(p, keep)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keep": keep, "items": items})
}

// cleanupRun removes the chosen tags — but only those the plan, worked out
// again right now, still says to remove. A container started since the
// dialog opened, or a newer build, takes a tag out of the list by itself.
func (a *api) cleanupRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID string   `json:"projectId"`
		Keep      int      `json:"keep"`
		Refs      []string `json:"refs"`
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
	items, err := a.cleanupPlan(p, keepParam(strconv.Itoa(req.Keep)))
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	removable := map[string]bool{}
	for _, it := range items {
		if it.Remove {
			removable[it.Ref] = true
		}
	}
	type result struct {
		Ref   string `json:"ref"`
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	out := []result{}
	for _, ref := range req.Refs {
		if !removable[ref] {
			out = append(out, result{Ref: ref, Error: "không còn nằm trong danh sách được xoá — để nguyên"})
			continue
		}
		if err := a.store.removeImage(ref); err != nil {
			out = append(out, result{Ref: ref, Error: err.Error()})
			continue
		}
		out = append(out, result{Ref: ref, OK: true})
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}
