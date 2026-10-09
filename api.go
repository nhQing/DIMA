package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type api struct{ store *Store }

func (a *api) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/state", a.state)
	mux.HandleFunc("PUT /api/config", a.saveConfig)
	mux.HandleFunc("GET /api/config/versions", a.listVersions)
	mux.HandleFunc("GET /api/config/versions/{file}", a.readVersion)
	mux.HandleFunc("POST /api/config/versions/{file}/restore", a.restoreVersion)
	mux.HandleFunc("POST /api/pick-folder", a.pickFolder)
	mux.HandleFunc("POST /api/pick-file", a.pickFile)
	mux.HandleFunc("GET /api/inspect-folder", a.inspectFolder)
	mux.HandleFunc("GET /api/scan-folder", a.scanFolder)
	mux.HandleFunc("GET /api/git", a.gitInfo)
	mux.HandleFunc("GET /api/build-vars", a.buildVars)
	mux.HandleFunc("GET /api/images", a.listImages)
	mux.HandleFunc("POST /api/images/import", a.importImages)
	mux.HandleFunc("POST /api/build", a.startBuild)
	mux.HandleFunc("GET /api/build/{id}/log", a.buildLog)
	mux.HandleFunc("POST /api/build/{id}/push", a.pushBuild)
	mux.HandleFunc("POST /api/build/{id}/promote", a.promoteBuild)
	mux.HandleFunc("POST /api/build/{id}/cancel", a.cancelBuild)
	mux.HandleFunc("DELETE /api/build/{id}", a.deleteBuild)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// badTagChars are the characters docker will not accept in a tag.
const badTagChars = " \t/\\:"

// checkVersion validates the project version a build or promote is given.
func checkVersion(v string) (string, string) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", "chưa nhập version"
	}
	if strings.ContainsAny(v, badTagChars) {
		return "", "version không được chứa khoảng trắng hoặc dấu : / \\"
	}
	return v, ""
}

// buildView is a build plus the verdict of canPush, spelled out for the UI.
//
// The screen must not work this out for itself from status, pushed and kind:
// a second copy of the rule is a second chance to forget a status, and the
// copy that forgot canceled is what offered Push on a build whose tag still
// belonged to the previous image. One answer, computed server side, is also
// the answer the push endpoint enforces.
type buildView struct {
	Build
	CanPush     bool   `json:"canPush"`
	PushBlocked string `json:"pushBlocked"`
}

// buildViews needs cfg because canPushEnv's verdict can depend on the
// environment's current local-only setting, which canPush alone cannot see.
// A build whose environment has since been deleted falls back to its own
// OnlyLocal snapshot, and one with neither falls back to canPush's plain
// answer.
func buildViews(list []Build, cfg Config) []buildView {
	out := make([]buildView, 0, len(list))
	for _, b := range list {
		v := buildView{Build: b, CanPush: true}
		if err := canPushEnv(cfg, b); err != nil {
			v.CanPush, v.PushBlocked = false, err.Error()
		}
		out = append(out, v)
	}
	return out
}

func (a *api) state(w http.ResponseWriter, r *http.Request) {
	version, ok := a.store.DockerVersion()
	cfg := a.store.Config()
	writeJSON(w, http.StatusOK, map[string]any{
		"config":        cfg,
		"builds":        buildViews(a.store.Builds(), cfg),
		"dockerOk":      ok,
		"dockerVersion": version,
		"buildxOk":      a.store.BuildxOK(),
		"appVersion":    appVersion,
		"buildStamp":    buildStamp,
	})
}

func (a *api) saveConfig(w http.ResponseWriter, r *http.Request) {
	var c Config
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		fail(w, http.StatusBadRequest, "JSON không hợp lệ: "+err.Error())
		return
	}
	for _, p := range c.Projects {
		if strings.TrimSpace(p.Name) == "" {
			fail(w, http.StatusBadRequest, "mỗi dự án phải có tên")
			return
		}
		for _, e := range p.Envs {
			if strings.TrimSpace(e.Name) == "" {
				fail(w, http.StatusBadRequest, "môi trường trong dự án "+p.Name+" chưa có tên")
				return
			}
			// The image may be set on either level; what matters is the
			// value the environment ends up with.
			if strings.TrimSpace(p.Effective(e).Image) == "" {
				fail(w, http.StatusBadRequest, "môi trường "+e.Name+" chưa có tên image — khai ở dự án hoặc ở chính môi trường")
				return
			}
			if strings.ContainsAny(e.TagPrefix, badTagChars) {
				fail(w, http.StatusBadRequest, "tiền tố tag của "+e.Name+" không được chứa khoảng trắng hoặc dấu : / \\")
				return
			}
			// Unlike the folder check below, this can only be reached by an
			// edit the user just made: no configuration written before this
			// version can carry the combination, so refusing the save cannot
			// strand anyone's existing settings.
			if e.IsOnlyLocal && p.ReleaseMode == ModePromote && e.ID != "" && e.ID == p.SourceEnvID {
				fail(w, http.StatusBadRequest, "môi trường "+e.Name+" đang là môi trường nguồn của "+p.Name+
					" nên không đánh dấu chỉ chạy trên máy này được — promote lấy image từ registry, mà môi trường này thì không bao giờ đẩy lên. Chọn môi trường nguồn khác trước")
				return
			}
			// Deliberately NOT checked here: whether the build context
			// exists on disk. Saving rewrites the whole file, so refusing it
			// over one bad folder blocks every unrelated edit — including
			// deleting the very project that is broken. A folder can also
			// vanish on its own, and settings have to stay editable when it
			// does.
			//
			// A missing or wrong folder is caught where it matters instead:
			// startBuild refuses the build, and the settings form says what
			// is wrong while you type.
		}
	}
	if err := a.store.SaveConfig(c); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a.store.Config())
}

// pickFolder opens the desktop's own folder chooser. The UI runs in a
// browser, which is not allowed to know real paths, so the dialog has to be
// opened by this process and the chosen path handed back.
func (a *api) pickFolder(w http.ResponseWriter, r *http.Request) {
	path, err := pickFolder("Chọn thư mục mã nguồn của dự án")
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, inspectFolder(path, ""))
}

// pickFile opens the desktop's own file chooser, used for the .env file
// field. The dialog starts in the project's source folder (query param
// "dir") when one is given, and the path comes back relative to that folder
// when the chosen file sits under it — matching how Dockerfile is typed as
// a name relative to the context, not as an absolute path.
func (a *api) pickFile(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	path, err := pickFile("Chọn file .env", dir)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": relativeToOr(path, dir)})
}

// relativeToOr rewrites path relative to dir when it sits under dir;
// otherwise (no dir known, or the file lives outside it) the path is handed
// back untouched rather than with a leading "../../" nobody asked for.
func relativeToOr(path, dir string) string {
	if path == "" || dir == "" {
		return path
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return path
	}
	return filepath.ToSlash(rel)
}

// inspectFolder answers what the settings form needs to show about a path:
// does it exist, and which Dockerfiles are in it.
func (a *api) inspectFolder(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, inspectFolder(
		r.URL.Query().Get("path"), r.URL.Query().Get("dockerfile")))
}

type folderInfo struct {
	Path        string   `json:"path"`
	Exists      bool     `json:"exists"`
	IsDir       bool     `json:"isDir"`
	Dockerfiles []string `json:"dockerfiles"`
	HasWanted   bool     `json:"hasWanted"`
	Wanted      string   `json:"wanted"`
}

// dockerfileOr returns the Dockerfile name to use when none is configured.
func dockerfileOr(name string) string {
	if n := strings.TrimSpace(name); n != "" {
		return n
	}
	return "Dockerfile"
}

// envFileOr mirrors dockerfileOr: an env file left blank still expands to
// the plain ".env" a custom command is most likely to expect.
func envFileOr(path string) string {
	if p := strings.TrimSpace(path); p != "" {
		return p
	}
	return ".env"
}

func inspectFolder(path, dockerfile string) folderInfo {
	info := folderInfo{Path: path, Wanted: dockerfileOr(dockerfile), Dockerfiles: []string{}}
	if strings.TrimSpace(path) == "" {
		return info
	}
	st, err := os.Stat(path)
	if err != nil {
		return info
	}
	info.Exists = true
	info.IsDir = st.IsDir()
	if !info.IsDir {
		return info
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return info
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if n := e.Name(); n == "Dockerfile" || strings.HasPrefix(n, "Dockerfile.") ||
			strings.HasSuffix(strings.ToLower(n), ".dockerfile") {
			info.Dockerfiles = append(info.Dockerfiles, n)
		}
	}
	// The configured Dockerfile may sit in a subfolder, so check the path
	// rather than only the listing above.
	if _, err := os.Stat(filepath.Join(path, info.Wanted)); err == nil {
		info.HasWanted = true
	}
	return info
}

// buildVars serves the placeholders a custom build command may use, so the
// settings screen lists exactly what the builder substitutes.
func (a *api) buildVars(w http.ResponseWriter, r *http.Request) {
	type row struct {
		Name  string `json:"name"`
		About string `json:"about"`
	}
	out := make([]row, 0, len(BuildVars))
	for _, v := range BuildVars {
		out = append(out, row{Name: v.Name, About: v.About})
	}
	writeJSON(w, http.StatusOK, map[string]any{"vars": out})
}

// gitInfo reports the branch an environment would build from right now, so
// the build dialog can show it before anything is built rather than after.
func (a *api) gitInfo(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("path")
	if envID := r.URL.Query().Get("envId"); envID != "" {
		if p, e, ok := a.store.Config().FindEnv(envID); ok {
			dir = p.Effective(e).Context
		}
	}
	writeJSON(w, http.StatusOK, ReadGit(dir))
}

// scanHit is one buildable folder found under a parent folder.
type scanHit struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Dockerfile string `json:"dockerfile"`
	Depth      int    `json:"depth"`
}

// maxScanHits keeps a scan of something like C:\ from returning a list
// nobody could read.
const maxScanHits = 200

// scanFolder lists the buildable folders under a parent folder, so a whole
// tree of repositories can be added in one go instead of one at a time.
//
// It looks three levels down, which covers a flat folder of repositories,
// the common apps/<name> layout, and one grouping folder above either of
// them. Descending stops as soon as a folder turns out to be buildable, so
// a repository's own subfolders are never mistaken for separate projects.
func (a *api) scanFolder(w http.ResponseWriter, r *http.Request) {
	root := strings.TrimSpace(r.URL.Query().Get("path"))
	if root == "" {
		fail(w, http.StatusBadRequest, "chưa chọn thư mục để quét")
		return
	}
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		fail(w, http.StatusBadRequest, "không tìm thấy thư mục: "+root)
		return
	}

	hits := []scanHit{}
	// The parent folder may itself be a project.
	if df := dockerfileIn(root); df != "" {
		hits = append(hits, scanHit{Name: filepath.Base(root), Path: root, Dockerfile: df, Depth: 0})
	}
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > 3 || len(hits) >= maxScanHits {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() || len(hits) >= maxScanHits || skipDir(e.Name()) {
				continue
			}
			sub := filepath.Join(dir, e.Name())
			if df := dockerfileIn(sub); df != "" {
				hits = append(hits, scanHit{Name: e.Name(), Path: sub, Dockerfile: df, Depth: depth})
				continue // a buildable folder is a project, not a container of projects
			}
			walk(sub, depth+1)
		}
	}
	walk(root, 1)

	writeJSON(w, http.StatusOK, map[string]any{
		"root":      root,
		"group":     filepath.Base(root),
		"hits":      hits,
		"truncated": len(hits) >= maxScanHits,
	})
}

// skipDir keeps the scan out of folders that never hold a project of their own.
func skipDir(name string) bool {
	switch name {
	case "node_modules", "vendor", "dist", "build", "target", "bin", "obj",
		".git", ".idea", ".vscode", ".venv", "venv", "__pycache__":
		return true
	}
	return strings.HasPrefix(name, ".")
}

// dockerfileIn returns the Dockerfile to use for a folder, or "".
func dockerfileIn(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, "Dockerfile")); err == nil {
		return "Dockerfile"
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if n := e.Name(); strings.HasPrefix(n, "Dockerfile.") ||
			strings.HasSuffix(strings.ToLower(n), ".dockerfile") {
			return n
		}
	}
	return ""
}

// listImages reports the repositories already on this machine, so work that
// predates DIMA can be pulled in instead of retyped.
func (a *api) listImages(w http.ResponseWriter, r *http.Request) {
	images, err := a.store.LocalImages()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	repos := GroupImages(images)

	// Mark what is already covered, so the screen can grey those out rather
	// than let the same repository be imported twice.
	known := map[string]bool{}
	for _, p := range a.store.Config().Projects {
		for _, e := range p.Envs {
			known[strings.ToLower(p.Effective(e).Repo())] = true
		}
	}
	type row struct {
		ImageRepo
		Known bool `json:"known"`
	}
	out := make([]row, 0, len(repos))
	for _, rp := range repos {
		out = append(out, row{ImageRepo: rp, Known: known[strings.ToLower(rp.Repository)]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": out})
}

// importImages turns chosen repositories into projects, and records the
// tags they already carry as history entries — without those the imported
// project would claim to have no versions while the images sit right there
// on the machine.
func (a *api) importImages(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Group        string   `json:"group"`
		Repositories []string `json:"repositories"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "JSON không hợp lệ")
		return
	}
	if len(req.Repositories) == 0 {
		fail(w, http.StatusBadRequest, "chưa chọn image nào")
		return
	}

	images, err := a.store.LocalImages()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	wanted := map[string]bool{}
	for _, name := range req.Repositories {
		wanted[name] = true
	}

	cfg := a.store.Config()
	added, recorded := 0, 0
	for _, repo := range GroupImages(images) {
		if !wanted[repo.Repository] {
			continue
		}
		p := Project{
			ID: newID(), Name: repo.Name, Group: strings.TrimSpace(req.Group),
			ReleaseMode: ModeRebuild,
			Registry:    repo.Registry, Image: repo.Image,
			BuildArgs: []KV{}, Labels: []KV{},
			Envs: repo.EnvsFor(),
		}
		cfg.Projects = append(cfg.Projects, p)
		added++
		recorded += a.recordExistingTags(p, repo)
	}
	if added == 0 {
		fail(w, http.StatusBadRequest, "không tìm thấy image nào trong số đã chọn")
		return
	}
	if err := a.store.SaveConfig(cfg); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"projects": added, "versions": recorded, "config": a.store.Config(),
	})
}

// recordExistingTags writes one history entry per version tag found on the
// machine, attributed to the environment whose prefix matches.
//
// repo.Tags was classified by GroupImages with the ordinary digit rule,
// before these environments existed. That rule throws away tags with no
// digit in them — latest, cache, local — because for an environment that
// ships, those are moving labels rather than releases.
//
// A local-only environment inverts that: cache IS the release its workflow
// produces, and there will never be a numbered one. So a tag the digit rule
// rejected is offered to the local-only environments, under
// classifyTagLocalOnly, before being discarded. Tags that ARE versions keep
// going to the environment whose prefix matches, exactly as before — a Cache
// environment does not get to swallow staging-1.2.3.
//
// No digest is required, same as for any other imported tag: a local-only
// environment never pushes, so it never has one, and demanding one would
// leave it permanently "chưa có bản nào".
func (a *api) recordExistingTags(p Project, repo ImageRepo) int {
	byPrefix := map[string]Env{}
	local := []Env{}
	for _, e := range p.Envs {
		if e.IsOnlyLocal {
			local = append(local, e)
			continue
		}
		byPrefix[e.TagPrefix] = e
	}
	n := 0
	for _, t := range repo.Tags {
		prefix, version, isVersion := t.Prefix, t.Version, t.IsVersion
		env, ok := byPrefix[prefix]
		if !isVersion || !ok {
			// Not a release by the ordinary rule, or a prefix no shipping
			// environment claims. A local-only environment takes it whole.
			if len(local) == 0 {
				continue
			}
			env, ok = local[0], true
			_, version, isVersion = classifyTagLocalOnly(t.Tag)
		}
		if !isVersion || !ok {
			continue
		}
		a.store.addBuild(Build{
			ID:          newID(),
			ProjectID:   p.ID,
			ProjectName: p.Name,
			EnvID:       env.ID,
			EnvName:     env.Name,
			Version:     version,
			Tag:         t.Tag,
			Ref:         repo.Repository + ":" + t.Tag,
			Kind:        KindImport,
			OnlyLocal:   env.IsOnlyLocal,
			Status:      "success",
			Command: "đọc từ danh sách image có sẵn trên máy (" + t.ID + ", " +
				t.Size + ", " + t.Age + ") — không phải do DIMA build",
			// Dating the entry by when the image was actually created is what
			// puts the version table in the right order.
			StartedAt:  whenBuilt(t),
			FinishedAt: whenBuilt(t),
		})
		n++
	}
	return n
}

// whenBuilt is the image's creation time, or now if docker gave a timestamp
// this build of DIMA could not parse.
func whenBuilt(t ImageTag) time.Time {
	if t.CreatedAt.IsZero() {
		return time.Now()
	}
	return t.CreatedAt
}

// checkBuildable reports why an environment could not be built, or "".
func checkBuildable(p Project, e Env) string {
	env := p.Effective(e)
	ctx := strings.TrimSpace(env.Context)
	if ctx == "" {
		return "môi trường " + e.Name + " chưa chọn thư mục mã nguồn — mở Cài đặt dự án và bấm “Chọn thư mục”"
	}
	// A relative path would resolve against wherever this process happens to
	// be running — the install folder, when opened from the Start Menu. It
	// is never what the person meant.
	if !filepath.IsAbs(ctx) {
		return "thư mục mã nguồn của " + e.Name + " đang là đường dẫn tương đối (" + ctx +
			"), phải là đường dẫn đầy đủ — bấm “Chọn thư mục” để chọn lại"
	}
	st, err := os.Stat(ctx)
	if err != nil {
		return "không tìm thấy thư mục mã nguồn của " + e.Name + ": " + ctx
	}
	if !st.IsDir() {
		return ctx + " là một file, không phải thư mục"
	}
	// With a build command of its own, what the folder needs is the script's
	// business, not ours. Only the folder itself had to be real — and the
	// command must not still name a placeholder that no longer exists.
	if bc := strings.TrimSpace(env.BuildCommand); bc != "" {
		if msg := CheckBuildCommand(bc); msg != "" {
			return "môi trường " + e.Name + ": " + msg
		}
		return ""
	}
	want := dockerfileOr(env.Dockerfile)
	if _, err := os.Stat(filepath.Join(ctx, want)); err != nil {
		return "không thấy " + want + " trong " + ctx
	}
	return ""
}

func (a *api) listVersions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.Snapshots())
}

func (a *api) readVersion(w http.ResponseWriter, r *http.Request) {
	raw, err := a.store.ReadSnapshot(r.PathValue("file"))
	if err != nil {
		fail(w, http.StatusNotFound, "không đọc được bản lưu")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(raw)
}

func (a *api) restoreVersion(w http.ResponseWriter, r *http.Request) {
	c, err := a.store.RestoreSnapshot(r.PathValue("file"))
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (a *api) startBuild(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EnvID   string `json:"envId"`
		Version string `json:"version"`
		Push    bool   `json:"push"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "JSON không hợp lệ")
		return
	}
	project, env, found := a.store.Config().FindEnv(req.EnvID)
	if !found {
		fail(w, http.StatusNotFound, "không tìm thấy môi trường")
		return
	}
	if !project.CanBuild(env.ID) {
		fail(w, http.StatusConflict,
			"dự án "+project.Name+" chạy theo kiểu build một lần rồi promote, nên chỉ build được ở môi trường nguồn")
		return
	}
	// Refused here rather than quietly building without the push: a request
	// that asked for push:true and got a 200 back with no error looks exactly
	// like a request whose push succeeded, and the person walks away
	// believing an image reached a registry that this project never touches.
	if req.Push && env.IsOnlyLocal {
		fail(w, http.StatusConflict,
			"môi trường "+env.Name+" được đánh dấu chỉ chạy trên máy này nên không push được — bỏ tick push khi build, hoặc bỏ đánh dấu đó trong cài đặt môi trường")
		return
	}
	// Fail here with something readable, rather than letting docker fail in
	// whatever directory this process happens to be running in.
	if msg := checkBuildable(project, env); msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return
	}
	version := strings.TrimSpace(req.Version)
	if version == "" {
		version = env.LastVersion
	}
	version, msg := checkVersion(version)
	if msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return
	}
	writeJSON(w, http.StatusOK, a.store.Start(project, env, version, req.Push))
}

// promoteBuild re-tags the image of an existing build for another
// environment of the same project.
func (a *api) promoteBuild(w http.ResponseWriter, r *http.Request) {
	src, ok := a.store.Build(r.PathValue("id"))
	if !ok {
		fail(w, http.StatusNotFound, "không tìm thấy bản build nguồn")
		return
	}
	var req struct {
		EnvID   string `json:"envId"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "JSON không hợp lệ")
		return
	}
	// Checked before the status/digest gate below: a local-only
	// environment's imported rows are routinely "success" with no digest,
	// and that gate would answer "push trước đã" — technically the reason
	// there is no digest, but it sends the person off to push something that
	// can never be pushed. The real reason is that this image never reaches
	// a registry at all, so there is nothing promote could re-tag.
	if onlyLocal(a.store.Config(), src) {
		fail(w, http.StatusConflict,
			"môi trường "+src.EnvName+" chỉ chạy trên máy này nên không promote đi đâu được — promote cần một bản đã push lên registry để re-tag, và môi trường này không bao giờ push; muốn đưa bản này sang môi trường khác thì build lại (rebuild) ở đó")
		return
	}
	// Only an image that actually reached the registry can be re-tagged
	// there; without a digest there is nothing to point the new tag at.
	if src.Status != "success" {
		fail(w, http.StatusConflict, "chỉ promote được bản build thành công")
		return
	}
	if !src.Pushed || src.Digest == "" {
		fail(w, http.StatusConflict, "bản này chưa được push lên registry nên chưa có digest để promote — push trước đã")
		return
	}
	project, env, found := a.store.Config().FindEnv(req.EnvID)
	if !found {
		fail(w, http.StatusNotFound, "không tìm thấy môi trường đích")
		return
	}
	if project.ID != src.ProjectID {
		fail(w, http.StatusBadRequest, "chỉ promote được trong cùng một dự án")
		return
	}
	if env.ID == src.EnvID {
		fail(w, http.StatusBadRequest, "bản này đã ở "+env.Name+" rồi")
		return
	}
	// Promote writes the new tag on the registry, so a local-only
	// environment cannot be the destination either: carrying out the request
	// would push an image the person marked as never leaving this machine.
	if env.IsOnlyLocal {
		fail(w, http.StatusConflict,
			"môi trường "+env.Name+" được đánh dấu chỉ chạy trên máy này nên không promote vào đó được — promote gắn tag mới ngay trên registry, tức là đẩy image lên; muốn có bản ở đó thì build tại chính môi trường đó")
		return
	}
	version := strings.TrimSpace(req.Version)
	if version == "" {
		version = src.Version
	}
	version, msg := checkVersion(version)
	if msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return
	}
	// Two environments of one project share a repository, so they are only
	// distinguishable by their tag prefix. Without one the promote would
	// re-point the source's own tag at itself and change nothing.
	if dst := project.Effective(env).Ref(version); dst == src.Ref {
		fail(w, http.StatusConflict,
			"tag đích trùng hệt tag nguồn ("+dst+") nên promote sẽ không có tác dụng — đặt tiền tố tag khác nhau cho "+
				src.EnvName+" và "+env.Name+" trong cài đặt môi trường")
		return
	}
	writeJSON(w, http.StatusOK, a.store.Promote(project, env, src, version))
}

func (a *api) buildLog(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	b, ok := a.store.Build(id)
	if !ok {
		fail(w, http.StatusNotFound, "không tìm thấy bản build")
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	chunk, next := a.store.LogSince(id, offset)
	writeJSON(w, http.StatusOK, map[string]any{
		"build": b, "chunk": chunk, "offset": next,
	})
}

func (a *api) pushBuild(w http.ResponseWriter, r *http.Request) {
	b, ok := a.store.Build(r.PathValue("id"))
	if !ok {
		fail(w, http.StatusNotFound, "không tìm thấy bản build")
		return
	}
	// Refused before the store, the log or docker is touched, so a build the
	// UI should not have offered Push for comes back unchanged in every
	// respect — same status, still no digest, still not marked pushed.
	//
	// The environment can have been deleted out from under this build;
	// onlyLocal falls back to the build's own snapshot when it has.
	if err := canPushEnv(a.store.Config(), b); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	workdir := ""
	if p, e, found := a.store.Config().FindEnv(b.EnvID); found {
		workdir = p.Effective(e).Context
	}
	if err := a.store.PushExisting(b, workdir); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	updated, _ := a.store.Build(b.ID)
	writeJSON(w, http.StatusOK, updated)
}

func (a *api) cancelBuild(w http.ResponseWriter, r *http.Request) {
	if !a.store.Cancel(r.PathValue("id")) {
		fail(w, http.StatusConflict, "bản build này không còn chạy")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "canceled"})
}

func (a *api) deleteBuild(w http.ResponseWriter, r *http.Request) {
	a.store.DeleteBuild(r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}
