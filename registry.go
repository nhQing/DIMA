package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Reading the registry is what turns the version table from "what DIMA built
// on this machine" into "what is actually out there": tags pushed by CI, by a
// colleague, or by this machine before DIMA existed.
//
// No docker command lists the tags of a remote repository, so this talks to
// the registry's HTTP API directly. Harbor's own API comes first because it
// answers everything in one call — tag, digest and push time. Any other
// registry gets the standard /v2/<name>/tags/list plus one HEAD per tag for
// the digest.
//
// Order of attempts: Harbor 2.x, Harbor 1.x, then the generic API.
//
// Credentials are never typed into DIMA. They are the ones `docker login`
// already stored, read back the way the docker CLI reads them.

// RemoteTag is one tag found on a registry.
type RemoteTag struct {
	Tag      string    `json:"tag"`
	Digest   string    `json:"digest"`
	PushedAt time.Time `json:"pushedAt"`
}

// credFunc returns the stored login for a registry host, or empty strings
// when there is none — anonymous pull is a legitimate way to read a public
// repository, so no login is not an error.
type credFunc func(host string) (user, secret string, err error)

type registryClient struct {
	http  *http.Client
	creds credFunc
	// scheme is https everywhere but in tests.
	scheme string
}

func newRegistryClient() *registryClient {
	return &registryClient{
		http: &http.Client{
			Timeout: 20 * time.Second,
			// Go keeps the Authorization header on a redirect to the same
			// host, even from https to http. A registry that redirects to
			// plain http does not get the password in clear text.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
					return fmt.Errorf("registry chuyển hướng sang http không mã hoá — DIMA từ chối")
				}
				if len(via) >= 10 {
					return fmt.Errorf("registry chuyển hướng quá nhiều lần")
				}
				return nil
			},
		},
		creds:  dockerCredentials,
		scheme: "https",
	}
}

// ---------- credentials ----------

type dockerConfigFile struct {
	Auths map[string]struct {
		Auth string `json:"auth"`
	} `json:"auths"`
	CredsStore  string            `json:"credsStore"`
	CredHelpers map[string]string `json:"credHelpers"`
}

func dockerConfigPath() string {
	if d := os.Getenv("DOCKER_CONFIG"); d != "" {
		return filepath.Join(d, "config.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".docker", "config.json")
}

// registryKey reduces the many spellings of a registry in config.json —
// "https://harbor.tech", "harbor.tech/", "harbor.tech" — to the host.
func registryKey(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	return s
}

// dockerCredentials finds the login `docker login <host>` stored, with the
// same precedence the docker CLI uses: a per-registry helper, then an inline
// auth entry, then the default credential store.
func dockerCredentials(host string) (string, string, error) {
	raw, err := os.ReadFile(dockerConfigPath())
	if err != nil {
		return "", "", nil // no docker config: anonymous
	}
	var cfg dockerConfigFile
	if err := json.Unmarshal(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")), &cfg); err != nil {
		return "", "", fmt.Errorf("không đọc được %s: %w", dockerConfigPath(), err)
	}
	key := registryKey(host)
	for reg, helper := range cfg.CredHelpers {
		if registryKey(reg) == key {
			return credentialHelper(helper, host)
		}
	}
	for reg, a := range cfg.Auths {
		if registryKey(reg) != key || a.Auth == "" {
			continue
		}
		dec, err := base64.StdEncoding.DecodeString(a.Auth)
		if err != nil {
			return "", "", fmt.Errorf("mục đăng nhập của %s trong config.json hỏng", host)
		}
		user, pass, _ := strings.Cut(string(dec), ":")
		return user, pass, nil
	}
	if cfg.CredsStore != "" {
		return credentialHelper(cfg.CredsStore, host)
	}
	return "", "", nil
}

// credentialHelper asks docker-credential-<name> for a login. A helper that
// knows nothing about the host answers with an error, which means anonymous.
func credentialHelper(name, host string) (string, string, error) {
	bin := "docker-credential-" + name
	var out bytes.Buffer
	cmd := exec.Command(bin, "get")
	hideWindow(cmd)
	cmd.Stdin = strings.NewReader(host)
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		if _, lookErr := exec.LookPath(bin); lookErr != nil {
			return "", "", fmt.Errorf("không tìm thấy %s — docker lưu mật khẩu qua nó, nên hãy kiểm tra Docker còn cài và nằm trong PATH", bin)
		}
		return "", "", nil
	}
	var got struct{ Username, Secret string }
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		return "", "", fmt.Errorf("%s trả về dữ liệu lạ: %w", bin, err)
	}
	return got.Username, got.Secret, nil
}

// ---------- HTTP with registry auth ----------

// do sends a request with whatever the registry asks for: nothing, Basic, or
// the Bearer token dance of the distribution spec. Basic credentials are
// only ever sent once the server has asked for them.
func (c *registryClient) do(host string, req *http.Request) (*http.Response, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	resp.Body.Close()

	user, secret, err := c.creds(host)
	if err != nil {
		return nil, err
	}
	retry := req.Clone(req.Context())
	scheme, params := parseChallenge(challenge)
	switch scheme {
	case "bearer":
		token, err := c.token(host, params, user, secret)
		if err != nil {
			return nil, err
		}
		retry.Header.Set("Authorization", "Bearer "+token)
	default:
		if user == "" {
			return nil, errNoLogin(host)
		}
		retry.SetBasicAuth(user, secret)
	}
	resp, err = c.http.Do(retry)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		if user == "" {
			return nil, errNoLogin(host)
		}
		return nil, fmt.Errorf("%s từ chối thông tin đăng nhập đã lưu — chạy lại `docker login %s`", host, host)
	}
	return resp, nil
}

func errNoLogin(host string) error {
	return fmt.Errorf("%s đòi đăng nhập mà máy này chưa lưu thông tin nào — chạy `docker login %s` một lần trong terminal", host, host)
}

// parseChallenge reads `Bearer realm="...",service="...",scope="..."`.
func parseChallenge(h string) (string, map[string]string) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	params := map[string]string{}
	for rest != "" {
		var part string
		// Split on commas outside quotes: a scope can list several actions.
		inQuote := false
		i := 0
		for ; i < len(rest); i++ {
			if rest[i] == '"' {
				inQuote = !inQuote
			}
			if rest[i] == ',' && !inQuote {
				break
			}
		}
		part, rest = rest[:i], strings.TrimPrefix(rest[i:], ",")
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok {
			params[strings.ToLower(k)] = strings.Trim(v, `"`)
		}
	}
	return strings.ToLower(scheme), params
}

// token fetches a Bearer token from the realm the registry named.
//
// The realm is whatever the registry's 401 says, so it is not trusted with
// the stored password blindly: the password only goes over https (when the
// registry itself is reached over https), and only to the registry's own
// host or a host under the same parent domain — registry.gitlab.com hands
// out tokens from gitlab.com, Harbor from itself. Anywhere else gets the
// request without credentials, which is enough for a public repository.
func (c *registryClient) token(host string, params map[string]string, user, secret string) (string, error) {
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("registry đòi token nhưng không nói lấy ở đâu")
	}
	ru, err := url.Parse(realm)
	if err != nil || ru.Host == "" {
		return "", fmt.Errorf("registry đưa địa chỉ lấy token không hợp lệ: %s", realm)
	}
	if c.scheme == "https" && ru.Scheme != "https" {
		return "", fmt.Errorf("registry %s đòi gửi mật khẩu qua %s không mã hoá — DIMA từ chối", host, realm)
	}
	if !sameSite(ru.Hostname(), registryKey(host)) {
		user, secret = "", ""
	}
	q := url.Values{}
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	if s := params["scope"]; s != "" {
		q.Set("scope", s)
	}
	req, err := http.NewRequest(http.MethodGet, realm+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	if user != "" {
		req.SetBasicAuth(user, secret)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("lấy token từ %s lỗi: HTTP %d", realm, resp.StatusCode)
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", err
	}
	return firstNonEmpty(tok.Token, tok.AccessToken), nil
}

// sameSite reports whether two hosts are the same, or share a parent domain
// of at least two labels (gitlab.com for registry.gitlab.com). A port on the
// registry key is ignored.
func sameSite(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	if h, _, ok := strings.Cut(b, ":"); ok {
		b = h
	}
	if a == b {
		return true
	}
	parent := func(h string) string {
		parts := strings.Split(h, ".")
		if len(parts) < 2 {
			return h
		}
		return strings.Join(parts[len(parts)-2:], ".")
	}
	pa := parent(a)
	return strings.Contains(pa, ".") && pa == parent(b)
}

func (c *registryClient) get(host, path string, headers map[string]string, method string) (*http.Response, error) {
	req, err := http.NewRequest(method, c.scheme+"://"+host+path, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return c.do(host, req)
}

// ---------- listing tags ----------

// errNotHarbor means the Harbor API is not there, so the generic one is next.
var errNotHarbor = fmt.Errorf("not harbor")

// ListTags reads every tag of one repository, e.g. harbor.tech/mmt/admin.
func (c *registryClient) ListTags(repo string) ([]RemoteTag, error) {
	host, name := splitRepository(repo)
	if host == "" {
		return nil, fmt.Errorf("%s không có registry riêng — DIMA chỉ đọc registry của bạn, không đọc Docker Hub", repo)
	}
	tags, err := c.harborTags(host, name)
	if err == errNotHarbor {
		tags, err = c.harborV1Tags(host, name)
	}
	if err == errNotHarbor {
		tags, err = c.genericTags(host, name)
	}
	if err != nil {
		return nil, err
	}
	// Stable: tags without a push time keep the order the lister gave them.
	sort.SliceStable(tags, func(i, j int) bool { return tags[i].PushedAt.After(tags[j].PushedAt) })
	return tags, nil
}

// harborTags uses Harbor's API: GET /api/v2.0/projects/{p}/repositories/{r}/artifacts.
// A repository below the project ("mmt/team/app") is one name to Harbor, so
// its slashes are encoded twice — Harbor decodes the path once itself.
func (c *registryClient) harborTags(host, name string) ([]RemoteTag, error) {
	project, repo, ok := strings.Cut(name, "/")
	if !ok {
		return nil, errNotHarbor // Harbor always has a project segment
	}
	repoEnc := url.PathEscape(url.PathEscape(repo))
	out := []RemoteTag{}
	const pageSize = 100
	for page := 1; page <= 50; page++ {
		path := fmt.Sprintf("/api/v2.0/projects/%s/repositories/%s/artifacts?with_tag=true&page_size=%d&page=%d",
			url.PathEscape(project), repoEnc, pageSize, page)
		resp, err := c.get(host, path, map[string]string{"Accept": "application/json"}, http.MethodGet)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusNotFound && page == 1 {
			// Either not a Harbor at all, or a Harbor without this repository.
			// The generic API tells the two apart with a clearer answer.
			resp.Body.Close()
			return nil, errNotHarbor
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("Harbor %s trả về HTTP %d cho %s", host, resp.StatusCode, name)
		}
		var arts []struct {
			Digest   string    `json:"digest"`
			PushTime time.Time `json:"push_time"`
			Tags     []struct {
				Name     string    `json:"name"`
				PushTime time.Time `json:"push_time"`
			} `json:"tags"`
		}
		err = json.NewDecoder(resp.Body).Decode(&arts)
		resp.Body.Close()
		if err != nil {
			// An HTML page or some other JSON: this is not Harbor's API.
			if page == 1 {
				return nil, errNotHarbor
			}
			return nil, err
		}
		for _, a := range arts {
			for _, t := range a.Tags {
				when := t.PushTime
				if when.IsZero() {
					when = a.PushTime
				}
				out = append(out, RemoteTag{Tag: t.Name, Digest: a.Digest, PushedAt: when})
			}
		}
		if len(arts) < pageSize {
			break
		}
	}
	return out, nil
}

// harborV1Tags uses the API of Harbor 1.x: GET /api/repositories/{name}/tags,
// with the whole repository name as one segment. Harbor 1.x is still common
// on self-hosted machines — harbor.tech runs 1.10 — and it is also the one
// that refuses manifest lists, which is why promote has to copy manifests
// as they are (see promoteSteps).
func (c *registryClient) harborV1Tags(host, name string) ([]RemoteTag, error) {
	path := "/api/repositories/" + url.PathEscape(name) + "/tags"
	resp, err := c.get(host, path, map[string]string{"Accept": "application/json"}, http.MethodGet)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errNotHarbor
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Harbor %s trả về HTTP %d cho %s", host, resp.StatusCode, name)
	}
	var list []struct {
		Name     string    `json:"name"`
		Digest   string    `json:"digest"`
		PushTime time.Time `json:"push_time"`
		Created  time.Time `json:"created"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, errNotHarbor
	}
	out := make([]RemoteTag, 0, len(list))
	for _, t := range list {
		when := t.PushTime
		if when.IsZero() {
			when = t.Created
		}
		out = append(out, RemoteTag{Tag: t.Name, Digest: t.Digest, PushedAt: when})
	}
	return out, nil
}

// manifestAccept lists every manifest type, so a HEAD answers with the digest
// docker itself would pull — an index for a multi-arch image.
const manifestAccept = "application/vnd.oci.image.index.v1+json, " +
	"application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.oci.image.manifest.v1+json, " +
	"application/vnd.docker.distribution.manifest.v2+json"

// maxDigestLookups caps the HEAD requests a generic registry costs: one per
// tag. The newest tags are the ones anyone promotes.
const maxDigestLookups = 200

func (c *registryClient) genericTags(host, name string) ([]RemoteTag, error) {
	resp, err := c.get(host, "/v2/"+name+"/tags/list?n=10000", map[string]string{"Accept": "application/json"}, http.MethodGet)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("không có repository %s trên %s", name, host)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s trả về HTTP %d cho %s", host, resp.StatusCode, name)
	}
	var body struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("%s không trả về danh sách tag: %w", host, err)
	}
	// The tags API gives no dates, so the newest-first order is a guess: a
	// tag sorted later by name is usually a later version.
	sort.Sort(sort.Reverse(sort.StringSlice(body.Tags)))
	out := make([]RemoteTag, 0, len(body.Tags))
	for i, t := range body.Tags {
		rt := RemoteTag{Tag: t}
		if i < maxDigestLookups {
			rt.Digest = c.headDigest(host, name, t)
		}
		out = append(out, rt)
	}
	return out, nil
}

func (c *registryClient) headDigest(host, name, tag string) string {
	resp, err := c.get(host, "/v2/"+name+"/manifests/"+url.PathEscape(tag), map[string]string{"Accept": manifestAccept}, http.MethodHead)
	if err != nil {
		return ""
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	return resp.Header.Get("Docker-Content-Digest")
}

// ---------- matching tags to environments ----------

// RegistryRow is one remote tag, placed in the environment it belongs to.
type RegistryRow struct {
	RemoteTag
	EnvID   string `json:"envId"`
	EnvName string `json:"envName"`
	Version string `json:"version"`
	Ref     string `json:"ref"`
	// Known means the history already has a successful record of this tag in
	// this environment; importing it again would only add a duplicate row.
	Known bool `json:"known"`
	// Drifted is the digest the history recorded for this tag when the
	// registry now serves a different one: someone re-pushed the tag from
	// elsewhere, and the history no longer describes what is deployed.
	Drifted string `json:"drifted,omitempty"`
}

// matchTags places each tag of one repository into the environment whose tag
// prefix it carries. The longest prefix wins, so "prod-v1.2" goes to the
// environment with prefix "prod-v" rather than one with "" — and a tag that
// fits no environment is returned separately rather than guessed at.
//
// A tag only counts as a version when what follows the prefix has a digit:
// latest, cache and stable are moving labels, not releases.
func matchTags(p Project, repo string, tags []RemoteTag, history []Build) ([]RegistryRow, []RemoteTag) {
	envs := []Env{}
	for _, e := range p.Envs {
		if e.IsOnlyLocal || p.Effective(e).Repo() != repo {
			continue
		}
		envs = append(envs, e)
	}
	// history is newest first, so the first record of a tag is the one that
	// last moved it.
	known := map[string]bool{}
	digestOf := map[string]string{}
	for _, b := range history {
		if b.Status != "success" {
			continue
		}
		k := b.EnvID + "\x00" + b.Tag
		if !known[k] {
			known[k] = true
			digestOf[k] = b.Digest
		}
	}
	rows, unmatched := []RegistryRow{}, []RemoteTag{}
	for _, t := range tags {
		best := envForTag(envs, t.Tag)
		if best == nil {
			unmatched = append(unmatched, t)
			continue
		}
		k := best.ID + "\x00" + t.Tag
		row := RegistryRow{
			RemoteTag: t,
			EnvID:     best.ID,
			EnvName:   best.Name,
			Version:   t.Tag[len(strings.TrimSpace(best.TagPrefix)):],
			Ref:       repo + ":" + t.Tag,
			Known:     known[k],
		}
		// Only a recorded digest can disagree: a record without one (read in
		// from a local image) says nothing about what the registry holds.
		if d := digestOf[k]; d != "" && t.Digest != "" && d != t.Digest {
			row.Drifted = d
		}
		rows = append(rows, row)
	}
	return rows, unmatched
}

// envForTag finds the environment a tag belongs to: the one with the longest
// tag prefix the tag starts with, where what follows the prefix is a version
// (has a digit). A local-only environment also takes digit-less tags — cache,
// local — because for it those are the releases (see classifyTagLocalOnly),
// but only when no shipping environment claims the tag.
func envForTag(envs []Env, tag string) *Env {
	var best *Env
	for i := range envs {
		pre := strings.TrimSpace(envs[i].TagPrefix)
		if !strings.HasPrefix(tag, pre) || len(tag) == len(pre) {
			continue
		}
		if !envs[i].IsOnlyLocal && !strings.ContainsAny(tag[len(pre):], "0123456789") {
			continue
		}
		if best == nil || len(pre) > len(strings.TrimSpace(best.TagPrefix)) ||
			(len(pre) == len(strings.TrimSpace(best.TagPrefix)) && best.IsOnlyLocal && !envs[i].IsOnlyLocal) {
			best = &envs[i]
		}
	}
	return best
}

// projectRepos lists the distinct repositories a project's shipping
// environments push to. Usually one; an environment may override the image.
func projectRepos(p Project) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, e := range p.Envs {
		if e.IsOnlyLocal {
			continue
		}
		r := p.Effective(e).Repo()
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}
