package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// localOnlyProject is a project that never reaches a registry: it stands in
// for the user's real maimoney-builder:local / harbor.tech/mm/mai-money-website:cache
// case described in the task — RepoDigests always empty because nothing was
// ever pushed. Two environments so promote has somewhere to try to go.
func localOnlyProject() Project {
	return Project{
		ID: "p-local", Name: "maimoney-builder", Image: "maimoney-builder",
		Envs: []Env{
			{ID: "e-src", Name: "Dev", TagPrefix: "", IsOnlyLocal: true},
			{ID: "e-dst", Name: "Other", TagPrefix: "other-", IsOnlyLocal: true},
		},
	}
}

// mixedProject is the shape the flag exists for and the project-level
// version could not express: one project with a Cache environment that stays
// on this machine next to a Staging environment that ships.
func mixedProject() Project {
	return Project{
		ID: "p-mixed", Name: "admin-ui", Registry: "harbor.tech", Image: "mm/admin-ui",
		Envs: []Env{
			{ID: "e-mix-ship", Name: "Staging", TagPrefix: "staging-"},
			{ID: "e-mix-cache", Name: "Cache", TagPrefix: "", IsOnlyLocal: true},
		},
	}
}

// ordinaryProject is a normal, registry-shipping project, used to prove the
// old behaviour survives untouched alongside the new local-only branch.
func ordinaryProject() Project {
	return Project{
		ID: "p-normal", Name: "web", Registry: "harbor.tech", Image: "mm/web",
		Envs: []Env{
			{ID: "e-normal-src", Name: "Staging", TagPrefix: "staging-"},
			{ID: "e-normal-dst", Name: "Production", TagPrefix: "prod-"},
		},
	}
}

func postJSON(t *testing.T, url, body string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("gọi %s lỗi: %v", url, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// ---------- push ----------

// TestPushIsRefusedForLocalOnlyProject covers the case the task is about:
// an imported "cache"/"local" tag reads as a perfectly successful build, so
// without the project-level check push would sail through canPush and hand
// the person a docker push that has no registry credentials to speak of.
func TestPushIsRefusedForLocalOnlyProject(t *testing.T) {
	store, srv := newTestAPI(t)
	if err := store.SaveConfig(Config{Projects: []Project{localOnlyProject()}}); err != nil {
		t.Fatal(err)
	}
	b := Build{
		ID: newID(), ProjectID: "p-local", ProjectName: "maimoney-builder",
		EnvID: "e-src", EnvName: "Dev",
		Version: "local", Tag: "local", Ref: "maimoney-builder:local",
		Kind: KindImport, Status: "success", StartedAt: time.Now(),
	}
	store.addBuild(b)

	code, msg := postPush(t, srv.URL, b.ID)
	if code != http.StatusConflict {
		t.Fatalf("status = %d, muốn 409 cho dự án local-only", code)
	}
	if strings.TrimSpace(msg) == "" {
		t.Fatal("bị từ chối nhưng không nói lý do")
	}
	if !strings.Contains(msg, "maimoney-builder") || !strings.Contains(strings.ToLower(msg), "máy") {
		t.Errorf("lý do = %q, muốn nêu đúng là môi trường chỉ chạy trên máy", msg)
	}
	after, _ := store.Build(b.ID)
	if after.Pushed {
		t.Error("bản bị từ chối push vẫn bị đánh dấu đã push")
	}
}

// A build whose project still ships to a registry must keep working exactly
// as before this change — same status code, same underlying canPush answer.
func TestPushStillWorksForOrdinaryProject(t *testing.T) {
	store, srv := newTestAPI(t)
	if err := store.SaveConfig(Config{Projects: []Project{ordinaryProject()}}); err != nil {
		t.Fatal(err)
	}
	// Not-yet-pushed successful build: canPush alone would allow this, and
	// it still must once the project-level check is layered on top.
	b := Build{
		ID: newID(), ProjectID: "p-normal", ProjectName: "web",
		EnvID: "e-normal-src", EnvName: "Staging",
		Version: "1.0.0", Tag: "staging-1.0.0", Ref: "harbor.tech/mm/web:staging-1.0.0",
		Kind: KindBuild, Status: "canceled", StartedAt: time.Now(),
	}
	store.addBuild(b)

	// canceled build on an ordinary project: still refused, but for the old
	// reason (tag belongs to a previous build), not a local-only one.
	code, msg := postPush(t, srv.URL, b.ID)
	if code != http.StatusConflict {
		t.Fatalf("status = %d, muốn 409", code)
	}
	if strings.Contains(msg, "chỉ chạy trên máy") {
		t.Errorf("lý do = %q, dự án bình thường không được nêu lý do local-only", msg)
	}
}

// ---------- state ----------

// The state endpoint's canPush/pushBlocked pair has to stay in sync with
// what the push endpoint itself enforces — otherwise the UI would still
// offer a Push button for a local-only build that the endpoint refuses.
func TestStateReportsLocalOnlyBuildsAsNotPushable(t *testing.T) {
	store, srv := newTestAPI(t)
	if err := store.SaveConfig(Config{Projects: []Project{localOnlyProject()}}); err != nil {
		t.Fatal(err)
	}
	b := Build{
		ID: newID(), ProjectID: "p-local", ProjectName: "maimoney-builder",
		EnvID: "e-src", EnvName: "Dev",
		Version: "cache", Tag: "cache", Ref: "maimoney-builder:cache",
		Kind: KindImport, Status: "success", StartedAt: time.Now(),
	}
	store.addBuild(b)

	resp, err := http.Get(srv.URL + "/api/state")
	if err != nil {
		t.Fatalf("gọi state lỗi: %v", err)
	}
	defer resp.Body.Close()
	var state struct {
		Builds []struct {
			ID          string `json:"id"`
			CanPush     bool   `json:"canPush"`
			PushBlocked string `json:"pushBlocked"`
		} `json:"builds"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatalf("state trả JSON không đọc được: %v", err)
	}
	found := false
	for _, sb := range state.Builds {
		if sb.ID != b.ID {
			continue
		}
		found = true
		if sb.CanPush {
			t.Error("build của dự án local-only báo canPush = true")
		}
		if strings.TrimSpace(sb.PushBlocked) == "" {
			t.Error("build của dự án local-only không kèm lý do trong pushBlocked")
		}
	}
	if !found {
		t.Fatal("state không trả về build vừa thêm")
	}
}

// ---------- build ----------

// TestStartBuildRejectsPushTrueForLocalOnlyProject makes sure push:true is
// refused with a readable reason instead of silently dropped: a 200 with the
// flag quietly ignored looks identical, from the client's side, to a push
// that actually happened.
func TestStartBuildRejectsPushTrueForLocalOnlyProject(t *testing.T) {
	store, srv := newTestAPI(t)
	if err := store.SaveConfig(Config{Projects: []Project{localOnlyProject()}}); err != nil {
		t.Fatal(err)
	}

	code, body := postJSON(t, srv.URL+"/api/build",
		`{"envId":"e-src","version":"1.0.0","push":true}`)
	if code != http.StatusConflict {
		t.Fatalf("status = %d, muốn 409 khi push:true cho dự án local-only", code)
	}
	msg, _ := body["error"].(string)
	if strings.TrimSpace(msg) == "" {
		t.Fatal("từ chối nhưng không nói lý do")
	}
	if !strings.Contains(strings.ToLower(msg), "máy") {
		t.Errorf("lý do = %q, muốn nêu rõ dự án chỉ chạy trên máy", msg)
	}
	// Nothing should have started: a silently-dropped push is bad, but a
	// build that ran anyway despite the 409 would be worse.
	if len(store.Builds()) != 0 {
		t.Error("build vẫn được tạo dù request bị từ chối")
	}
}

// A build request without push, or on an ordinary project, must be entirely
// unaffected by this guard — it only reads Project.IsOnlyLocal, never
// forces builds to fail for reasons unrelated to pushing.
func TestStartBuildAllowsPushFalseForLocalOnlyProject(t *testing.T) {
	store, srv := newTestAPI(t)
	p := localOnlyProject()
	// BuildCommand sidesteps the need for a real Dockerfile on disk — this
	// test only cares whether the push guard fires, not the build mechanics.
	p.BuildCommand = "echo ok"
	p.Context = t.TempDir()
	if err := store.SaveConfig(Config{Projects: []Project{p}}); err != nil {
		t.Fatal(err)
	}

	code, body := postJSON(t, srv.URL+"/api/build",
		`{"envId":"e-src","version":"1.0.0","push":false}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %v, muốn 200 khi không push", code, body)
	}
}

// ---------- Start (defense in depth) ----------

// Even if some caller reached Start with push=true for a local-only
// project — bypassing the HTTP guard — Start itself must not attempt a
// docker push. This is the belt to the HTTP handler's suspenders.
func TestStartNeverPushesForALocalOnlyProject(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	env := Env{ID: newID(), Name: "Dev"}
	p := Project{
		ID: newID(), Name: "tool", Image: "tool", IsOnlyLocal: true,
		Context: t.TempDir(), BuildCommand: "echo built",
		Envs: []Env{env},
	}

	started := s.Start(p, env, "1.0.0", true)

	deadline := time.Now().Add(10 * time.Second)
	var done Build
	for time.Now().Before(deadline) {
		if b, ok := s.Build(started.ID); ok && b.Status != "running" {
			done = b
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	// "echo built" không gắn tag nào, nên bản build này hỏng — đó là hành vi
	// đúng và không phải thứ test này kiểm. Điều cần giữ là: dù push=true,
	// dự án local-only không bao giờ chạy tới lệnh push.
	if done.Status != "failed" || !strings.Contains(done.Err, "tool:1.0.0") {
		t.Fatalf("status = %q, err = %q — muốn hỏng vì không tạo ra image", done.Status, done.Err)
	}
	if done.Pushed {
		t.Error("Start đã push cho dự án local-only dù push=true được truyền vào")
	}
	// Look for the actual push command line Start would have run, not just
	// the substring "push" — the "không thấy image" warning this custom
	// command also triggers happens to mention push, digest and promote in
	// prose and would otherwise make this assertion a false positive.
	log, _ := s.LogSince(started.ID, 0)
	if strings.Contains(log, "docker push tool:1.0.0") {
		t.Errorf("log chứa lệnh push, muốn nhánh push bị bỏ qua hoàn toàn:\n%s", log)
	}
}

// ---------- promote ----------

func TestPromoteBuildIsRefusedForLocalOnlyProject(t *testing.T) {
	store, srv := newTestAPI(t)
	if err := store.SaveConfig(Config{Projects: []Project{localOnlyProject()}}); err != nil {
		t.Fatal(err)
	}
	src := Build{
		ID: newID(), ProjectID: "p-local", ProjectName: "maimoney-builder",
		EnvID: "e-src", EnvName: "Dev",
		Version: "local", Tag: "local", Ref: "maimoney-builder:local",
		Kind: KindImport, Status: "success", StartedAt: time.Now(),
		// Deliberately no Pushed/Digest: an imported local-only tag never
		// has either, which is exactly what must not be the reason given.
	}
	store.addBuild(src)

	code, body := postJSON(t, srv.URL+"/api/build/"+src.ID+"/promote", `{"envId":"e-dst"}`)
	if code != http.StatusConflict {
		t.Fatalf("status = %d, muốn 409", code)
	}
	msg, _ := body["error"].(string)
	if strings.TrimSpace(msg) == "" {
		t.Fatal("bị từ chối nhưng không nói lý do")
	}
	if strings.Contains(msg, "chưa có digest") {
		t.Errorf("lý do = %q, không được đổ lỗi cho digest — dự án này chưa bao giờ push và sẽ không bao giờ", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "máy") {
		t.Errorf("lý do = %q, muốn nêu rõ dự án chỉ chạy trên máy", msg)
	}
}

// A pushed, successful build on an ordinary project must still promote
// exactly as before — this guard must not fire for it.
func TestPromoteBuildStillWorksForOrdinaryProject(t *testing.T) {
	store, srv := newTestAPI(t)
	if err := store.SaveConfig(Config{Projects: []Project{ordinaryProject()}}); err != nil {
		t.Fatal(err)
	}
	src := Build{
		ID: newID(), ProjectID: "p-normal", ProjectName: "web",
		EnvID: "e-normal-src", EnvName: "Staging",
		Version: "1.0.0", Tag: "staging-1.0.0", Ref: "harbor.tech/mm/web:staging-1.0.0",
		Digest: "sha256:abc", Pushed: true,
		Kind: KindBuild, Status: "success", StartedAt: time.Now(),
	}
	store.addBuild(src)

	code, body := postJSON(t, srv.URL+"/api/build/"+src.ID+"/promote", `{"envId":"e-normal-dst"}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %v, muốn 200", code, body)
	}
}

// ---------- canPushProject ----------

// The local-only verdict has to win even over a build state that canPush
// alone would also refuse, so the message the person sees names the actual
// reason (the environment) rather than a coincidental one (the build status).
func TestCanPushEnvRefusesLocalOnlyRegardlessOfBuildState(t *testing.T) {
	cfg := Config{Projects: []Project{{
		ID: "p", Name: "tool", Image: "tool",
		Envs: []Env{{ID: "e", Name: "Cache", IsOnlyLocal: true}},
	}}}
	for _, b := range []Build{
		{EnvID: "e", EnvName: "Cache", Ref: "tool:1.0.0", Status: "success"},
		{EnvID: "e", EnvName: "Cache", Ref: "tool:1.0.0", Status: "success", Pushed: true},
		{EnvID: "e", EnvName: "Cache", Ref: "tool:1.0.0", Status: "failed"},
		{EnvID: "e", EnvName: "Cache", Ref: ""},
	} {
		err := canPushEnv(cfg, b)
		if err == nil {
			t.Fatalf("canPushEnv(%+v) = nil, muốn bị từ chối", b)
		}
		if !strings.Contains(err.Error(), "chỉ chạy trên máy") {
			t.Errorf("lý do = %q, muốn nêu rõ là môi trường local-only", err.Error())
		}
	}
}

func TestCanPushEnvFallsBackToCanPushForOrdinaryEnvs(t *testing.T) {
	cfg := Config{Projects: []Project{ordinaryProject()}}
	b := Build{EnvID: "e-normal-src", EnvName: "Staging", Ref: "web:1.0.0", Status: "success"}
	if err := canPushEnv(cfg, b); err != nil {
		t.Errorf("môi trường bình thường, build thành công chưa push: muốn push được, nhận %v", err)
	}
}

// A build keeps its own answer after the environment it belonged to is gone.
// Without the snapshot, deleting the Cache environment would turn every old
// cache build into something the UI offers a Push button for.
func TestCanPushEnvUsesTheBuildSnapshotWhenTheEnvIsGone(t *testing.T) {
	cfg := Config{Projects: []Project{ordinaryProject()}}
	b := Build{EnvID: "khong-con-nua", EnvName: "Cache", Ref: "tool:cache",
		Status: "success", OnlyLocal: true}
	if err := canPushEnv(cfg, b); err == nil {
		t.Error("môi trường đã xóa nhưng bản build vẫn là local-only: không được cho push")
	}
}

// The point of moving the flag: one project, two environments, two answers.
func TestOneProjectCanShipFromOneEnvAndKeepAnotherLocal(t *testing.T) {
	cfg := Config{Projects: []Project{mixedProject()}}

	ship := Build{EnvID: "e-mix-ship", EnvName: "Staging",
		Ref: "harbor.tech/mm/admin-ui:staging-1.2.0", Status: "success"}
	if err := canPushEnv(cfg, ship); err != nil {
		t.Errorf("Staging phải push được, nhận %v", err)
	}

	cache := Build{EnvID: "e-mix-cache", EnvName: "Cache",
		Ref: "harbor.tech/mm/admin-ui:cache", Status: "success"}
	if err := canPushEnv(cfg, cache); err == nil {
		t.Error("Cache phải bị từ chối push")
	}
}

// ---------- classifyTag / recordExistingTags ----------

func TestClassifyTagLocalOnlyTreatsEveryTagAsAVersion(t *testing.T) {
	cases := []struct{ tag, version string }{
		{"cache", "cache"},
		{"local", "local"},
		{"latest", "latest"},
		{"stable", "stable"},
		{"1.2.3", "1.2.3"},
		// Local-only projects have no environment layer to distinguish by
		// prefix, so even something that looks like a prefixed tag is kept
		// whole rather than split.
		{"prod-1.2.3", "prod-1.2.3"},
	}
	for _, c := range cases {
		prefix, version, isVersion := classifyTagLocalOnly(c.tag)
		if !isVersion {
			t.Errorf("classifyTagLocalOnly(%q) isVersion = false, muốn true", c.tag)
		}
		if prefix != "" {
			t.Errorf("classifyTagLocalOnly(%q) prefix = %q, muốn rỗng", c.tag, prefix)
		}
		if version != c.version {
			t.Errorf("classifyTagLocalOnly(%q) version = %q, muốn %q", c.tag, version, c.version)
		}
	}
}

// recordExistingTags is what turns "docker images sees it" into a version
// row. For a local-only project it must keep tags classifyTag alone would
// have thrown away, since those non-digit tags are the only releases such a
// project ever has.
func TestRecordExistingTagsKeepsNonDigitTagsForLocalOnlyProject(t *testing.T) {
	store, srv := newTestAPI(t)
	_ = srv // API only used to construct `a`; the call below is direct.
	a := &api{store: store}

	p := localOnlyProject()
	repo := ImageRepo{
		Repository: "maimoney-builder",
		Tags: []ImageTag{
			// Classified the ordinary way by GroupImages, before this
			// project's IsOnlyLocal flag comes into play.
			{Tag: "local", Prefix: "", Version: "local", IsVersion: false},
			{Tag: "cache", Prefix: "", Version: "cache", IsVersion: false},
			{Tag: "latest", Prefix: "", Version: "latest", IsVersion: false},
		},
	}

	n := a.recordExistingTags(p, repo)
	if n != 3 {
		t.Fatalf("ghi %d bản, muốn cả 3 tag được ghi cho dự án local-only", n)
	}
	builds := store.Builds()
	if len(builds) != 3 {
		t.Fatalf("store có %d build, muốn 3", len(builds))
	}
	got := map[string]bool{}
	for _, b := range builds {
		got[b.Version] = true
		if b.Digest != "" {
			t.Errorf("build %q có digest %q, tag đọc vào không được đòi digest", b.Version, b.Digest)
		}
	}
	for _, want := range []string{"local", "cache", "latest"} {
		if !got[want] {
			t.Errorf("thiếu bản %q trong lịch sử", want)
		}
	}
}

// The same tags on an ordinary (non local-only) project must still be
// dropped exactly as before — this is the regression check that the new
// branch does not leak into the path every other project takes.
func TestRecordExistingTagsStillDropsNonDigitTagsForOrdinaryProject(t *testing.T) {
	store, srv := newTestAPI(t)
	_ = srv
	a := &api{store: store}

	p := ordinaryProject()
	repo := ImageRepo{
		Repository: "harbor.tech/mm/web",
		Tags: []ImageTag{
			{Tag: "latest", Prefix: "", Version: "latest", IsVersion: false},
			{Tag: "staging-1.2.3", Prefix: "staging-", Version: "1.2.3", IsVersion: true},
		},
	}

	n := a.recordExistingTags(p, repo)
	if n != 1 {
		t.Fatalf("ghi %d bản, muốn đúng 1 (chỉ tag có chữ số)", n)
	}
	builds := store.Builds()
	if len(builds) != 1 || builds[0].Version != "1.2.3" {
		t.Fatalf("build ghi lại = %+v, muốn đúng version 1.2.3", builds)
	}
}
