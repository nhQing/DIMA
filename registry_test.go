package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeHarbor answers Harbor's artifact API behind Basic auth, the way a
// private Harbor project does.
func fakeHarbor(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "robot" || p != "s3cret" {
			w.Header().Set("WWW-Authenticate", `Basic realm="harbor"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.EscapedPath() != "/api/v2.0/projects/mmt/repositories/admin-mmt/artifacts" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode([]map[string]any{
			{"digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "push_time": "2026-10-15T09:00:00Z",
				"tags": []map[string]any{{"name": "staging-v1.1.1", "push_time": "2026-10-15T09:00:00Z"},
					{"name": "prod-v1.1.1", "push_time": "2026-10-16T09:00:00Z"}}},
			{"digest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "push_time": "2026-10-18T09:00:00Z",
				"tags": []map[string]any{{"name": "staging-v1.2.0"}, {"name": "latest"}}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func clientFor(srv *httptest.Server, user, pass string) *registryClient {
	return &registryClient{
		http:   srv.Client(),
		scheme: "https",
		creds:  func(string) (string, string, error) { return user, pass, nil },
	}
}

func hostOf(srv *httptest.Server) string { return strings.TrimPrefix(srv.URL, "https://") }

func TestListTagsFromHarbor(t *testing.T) {
	srv := fakeHarbor(t)
	tags, err := clientFor(srv, "robot", "s3cret").ListTags(hostOf(srv) + "/mmt/admin-mmt")
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 4 {
		t.Fatalf("có %d tag, muốn 4: %+v", len(tags), tags)
	}
	got := map[string]RemoteTag{}
	for _, tg := range tags {
		got[tg.Tag] = tg
	}
	// Hai tag cùng một artifact phải cùng digest: chính là điều promote tạo ra.
	if got["staging-v1.1.1"].Digest != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || got["prod-v1.1.1"].Digest != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("digest sai: %+v", got)
	}
	// Tag không có giờ push riêng thì lấy giờ push của artifact.
	if got["staging-v1.2.0"].PushedAt.IsZero() {
		t.Error("tag không có push_time phải lấy giờ của artifact")
	}
	if tags[0].Tag != "staging-v1.2.0" && tags[0].Tag != "latest" {
		t.Errorf("phải xếp mới nhất trước, đang là %s", tags[0].Tag)
	}
}

func TestListTagsSaysToLoginWhenNoneIsStored(t *testing.T) {
	srv := fakeHarbor(t)
	_, err := clientFor(srv, "", "").ListTags(hostOf(srv) + "/mmt/admin-mmt")
	if err == nil || !strings.Contains(err.Error(), "docker login") {
		t.Errorf("err = %v, muốn nhắc chạy docker login", err)
	}
}

func TestListTagsRefusesDockerHub(t *testing.T) {
	_, err := newRegistryClient().ListTags("library/postgres")
	if err == nil {
		t.Error("image không có registry riêng thì không đọc")
	}
}

// fakeHarborV1 answers like harbor.tech, which runs Harbor 1.10: the v2.0
// API is an HTML 404 page, and tags live under /api/repositories/{name}/tags
// with the whole name as one escaped segment.
func fakeHarborV1(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := r.BasicAuth(); !ok {
			w.WriteHeader(http.StatusUnauthorized) // Harbor 1.x sends no challenge
			return
		}
		if r.URL.EscapedPath() == "/api/repositories/mmt%2Fadmin-mmt/tags" {
			w.Write([]byte(`[{"name":"prod-v1.1.1","digest":"sha256:a175","push_time":"2026-10-19T07:01:55.547446Z","created":"2026-10-19T07:01:52Z"},
				{"name":"v1.2.2","digest":"sha256:bb47","push_time":"0001-01-01T00:00:00Z","created":"2026-10-06T08:54:39Z"}]`))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("<!DOCTYPE HTML><title>404 - Page Not Found</title>"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestListTagsFromHarborV1(t *testing.T) {
	srv := fakeHarborV1(t)
	tags, err := clientFor(srv, "robot", "s3cret").ListTags(hostOf(srv) + "/mmt/admin-mmt")
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 || tags[0].Tag != "prod-v1.1.1" || tags[0].Digest != "sha256:a175" {
		t.Fatalf("tags = %+v", tags)
	}
	if tags[1].PushedAt.IsZero() {
		t.Error("không có push_time thì phải lấy created")
	}
}

// fakeRegistry is a plain distribution registry: no Harbor API, Bearer
// tokens from a separate endpoint, digests only through HEAD.
func fakeRegistry(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			if u, p, ok := r.BasicAuth(); !ok || u != "me" || p != "pw" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.URL.Query().Get("scope") != "repository:team/web:pull" {
				t.Errorf("scope = %q", r.URL.Query().Get("scope"))
			}
			json.NewEncoder(w).Encode(map[string]string{"token": "tok"})
		case strings.HasPrefix(r.URL.Path, "/v2/"):
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.Header().Set("WWW-Authenticate",
					`Bearer realm="`+srv.URL+`/token",service="reg",scope="repository:team/web:pull"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.URL.Path == "/v2/team/web/tags/list" {
				json.NewEncoder(w).Encode(map[string]any{"tags": []string{"s-1.0.0", "s-1.1.0"}})
				return
			}
			if strings.HasPrefix(r.URL.Path, "/v2/team/web/manifests/") && r.Method == http.MethodHead {
				if !strings.Contains(r.Header.Get("Accept"), "image.index") {
					t.Error("HEAD phải xin cả kiểu index để ra đúng digest của image đa kiến trúc")
				}
				w.Header().Set("Docker-Content-Digest", "sha256:"+strings.TrimPrefix(r.URL.Path, "/v2/team/web/manifests/"))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestListTagsFromAPlainRegistryWithBearerTokens(t *testing.T) {
	srv := fakeRegistry(t)
	tags, err := clientFor(srv, "me", "pw").ListTags(hostOf(srv) + "/team/web")
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 {
		t.Fatalf("tags = %+v", tags)
	}
	for _, tg := range tags {
		if tg.Digest != "sha256:"+tg.Tag {
			t.Errorf("%s: digest = %q", tg.Tag, tg.Digest)
		}
	}
}

// Mật khẩu docker login không được gửi tới nơi cấp token lạ hoặc qua http.
func TestTokenRealmIsNotTrustedBlindly(t *testing.T) {
	got := ""
	evil := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]string{"token": "t"})
	}))
	defer evil.Close()
	c := clientFor(evil, "me", "pw")
	// Realm 127.0.0.1 khác hẳn registry harbor.tech: đi lấy token không kèm mật khẩu.
	if _, err := c.token("harbor.tech", map[string]string{"realm": evil.URL + "/token"}, "me", "pw"); err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("đã gửi %q tới realm của host khác", got)
	}
	if _, err := c.token("harbor.tech", map[string]string{"realm": "http://harbor.tech/token"}, "me", "pw"); err == nil {
		t.Error("realm http không mã hoá phải bị từ chối")
	}
	for _, tc := range []struct {
		a, b string
		want bool
	}{{"harbor.tech", "harbor.tech:443", true}, {"gitlab.com", "registry.gitlab.com", true},
		{"evil.com", "harbor.tech", false}, {"127.0.0.1", "harbor.tech", false}} {
		if sameSite(tc.a, tc.b) != tc.want {
			t.Errorf("sameSite(%s, %s) != %v", tc.a, tc.b, tc.want)
		}
	}
}

// Registry không cho giờ push: tag nhập vào không được thành "bản hiện tại"
// vượt lên trên bản đã có, và giữ đúng thứ tự mới-trước.
func TestUndatedImportsSitBelowExistingHistory(t *testing.T) {
	reg := fakeRegistry(t)
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &api{store: store, registry: clientFor(reg, "me", "pw")}
	mux := http.NewServeMux()
	a.routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p := Project{ID: "p", Name: "web", Registry: hostOf(reg), Image: "team/web",
		Envs: []Env{{ID: "s", Name: "Staging", TagPrefix: "s-"}}}
	if err := store.SaveConfig(Config{Projects: []Project{p}}); err != nil {
		t.Fatal(err)
	}
	built := time.Now().Add(-time.Hour)
	store.addBuild(Build{ID: "mine", ProjectID: "p", EnvID: "s", Tag: "s-2.0.0", Version: "2.0.0", Status: "success", StartedAt: built})

	code, body := postJSON(t, srv.URL+"/api/registry/import", `{"projectId":"p","keys":["s|s-1.0.0","s|s-1.1.0"]}`)
	if code != http.StatusOK || body["added"].(float64) != 2 {
		t.Fatalf("status=%d body=%v", code, body)
	}
	list := store.Builds() // mới nhất trước
	order := []string{}
	for _, b := range list {
		order = append(order, b.Tag)
	}
	if strings.Join(order, ",") != "s-2.0.0,s-1.1.0,s-1.0.0" {
		t.Errorf("thứ tự = %v, muốn bản đã có đứng đầu rồi 1.1.0, 1.0.0", order)
	}
}

func TestParseChallenge(t *testing.T) {
	scheme, p := parseChallenge(`Bearer realm="https://h/service/token",service="harbor-registry",scope="repository:a/b:pull,push"`)
	if scheme != "bearer" || p["realm"] != "https://h/service/token" || p["service"] != "harbor-registry" ||
		p["scope"] != "repository:a/b:pull,push" {
		t.Errorf("scheme=%q params=%v", scheme, p)
	}
}

func TestDockerCredentialsReadsInlineAuth(t *testing.T) {
	dir := t.TempDir()
	auth := base64.StdEncoding.EncodeToString([]byte("robot:s3cret"))
	cfg := `{"auths":{"https://harbor.tech/":{"auth":"` + auth + `"}}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONFIG", dir)
	u, p, err := dockerCredentials("harbor.tech")
	if err != nil || u != "robot" || p != "s3cret" {
		t.Errorf("= %q %q %v", u, p, err)
	}
	if u, _, _ := dockerCredentials("other.tech"); u != "" {
		t.Error("registry khác không được nhận nhầm thông tin đăng nhập")
	}
}

// ---------- ghép tag vào môi trường ----------

func TestMatchTagsPicksTheLongestPrefix(t *testing.T) {
	p := Project{Registry: "harbor.tech", Image: "mmt/admin", Envs: []Env{
		{ID: "def", Name: "Mặc định", TagPrefix: ""},
		{ID: "prd", Name: "Production", TagPrefix: "prod-v"},
		{ID: "stg", Name: "Staging", TagPrefix: "staging-v"},
		{ID: "loc", Name: "Cache", TagPrefix: "", IsOnlyLocal: true},
	}}
	tags := []RemoteTag{{Tag: "prod-v1.1.1", Digest: "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}, {Tag: "staging-v1.2.0"}, {Tag: "1.0.0"}, {Tag: "latest"}, {Tag: "prod-vnext"}}
	history := []Build{{EnvID: "prd", Tag: "prod-v1.1.1", Status: "success", Digest: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}}
	rows, unmatched := matchTags(p, "harbor.tech/mmt/admin", tags, history)

	byTag := map[string]RegistryRow{}
	for _, r := range rows {
		byTag[r.Tag] = r
	}
	if byTag["prod-v1.1.1"].EnvID != "prd" || byTag["prod-v1.1.1"].Version != "1.1.1" || !byTag["prod-v1.1.1"].Known {
		t.Errorf("prod-v1.1.1 = %+v", byTag["prod-v1.1.1"])
	}
	// Lịch sử ghi một digest, registry đang giữ digest khác: tag đã bị đẩy đè.
	if byTag["prod-v1.1.1"].Drifted != "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd" {
		t.Errorf("Drifted = %q, muốn sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", byTag["prod-v1.1.1"].Drifted)
	}
	if byTag["staging-v1.2.0"].EnvID != "stg" {
		t.Errorf("staging-v1.2.0 = %+v", byTag["staging-v1.2.0"])
	}
	if byTag["1.0.0"].EnvID != "def" {
		t.Errorf("1.0.0 phải về môi trường không tiền tố, đang là %+v", byTag["1.0.0"])
	}
	// latest và prod-vnext không có chữ số sau tiền tố: không phải phiên bản.
	if len(unmatched) != 2 {
		t.Errorf("unmatched = %+v, muốn latest và prod-vnext", unmatched)
	}
	for _, r := range rows {
		if r.EnvID == "loc" {
			t.Error("môi trường chỉ-máy-này không bao giờ nhận tag từ registry")
		}
	}
}

// ---------- API ----------

func TestRegistryImportRecordsPromotableVersions(t *testing.T) {
	harbor := fakeHarbor(t)
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &api{store: store, registry: clientFor(harbor, "robot", "s3cret")}
	mux := http.NewServeMux()
	a.routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p := Project{ID: "p", Name: "admin", Registry: hostOf(harbor), Image: "mmt/admin-mmt", Envs: []Env{
		{ID: "stg", Name: "Staging", TagPrefix: "staging-v"},
		{ID: "prd", Name: "Production", TagPrefix: "prod-v"},
	}}
	if err := store.SaveConfig(Config{Projects: []Project{p}}); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/api/registry/tags?projectId=p")
	if err != nil {
		t.Fatal(err)
	}
	var listed struct{ Repos []registryRepo }
	json.NewDecoder(resp.Body).Decode(&listed)
	resp.Body.Close()
	if len(listed.Repos) != 1 || listed.Repos[0].Error != "" || len(listed.Repos[0].Rows) != 3 {
		t.Fatalf("repos = %+v", listed.Repos)
	}

	code, body := postJSON(t, srv.URL+"/api/registry/import",
		`{"projectId":"p","keys":["stg|staging-v1.2.0","prd|prod-v1.1.1"]}`)
	if code != http.StatusOK || body["added"].(float64) != 2 {
		t.Fatalf("status=%d body=%v", code, body)
	}
	var stg Build
	for _, b := range store.Builds() {
		if b.Tag == "staging-v1.2.0" {
			stg = b
		}
	}
	// Đủ điều kiện để promote: thành công, đã ở trên registry, có digest của registry.
	if stg.Kind != KindRegistry || stg.Status != "success" || !stg.Pushed || stg.Digest != "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Errorf("bản ghi = %+v", stg)
	}
	if !stg.StartedAt.Equal(time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("thời điểm phải là lúc push lên registry, đang là %v", stg.StartedAt)
	}

	// Nhập lại lần nữa không tạo bản trùng.
	_, body = postJSON(t, srv.URL+"/api/registry/import", `{"projectId":"p","keys":["stg|staging-v1.2.0"]}`)
	if body["added"].(float64) != 0 {
		t.Errorf("nhập lại tạo thêm %v bản ghi trùng", body["added"])
	}
}
