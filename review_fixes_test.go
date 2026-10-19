package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Tag lấy từ registry không được mang lệnh shell vào {ref}: registry độc hại
// đặt tên tag kiểu "1.0;calc", nhập vào lịch sử rồi bấm Build là chạy.
func TestRegistryTagsThatAreNotDockerTagsAreDropped(t *testing.T) {
	p := Project{Image: "app", Envs: []Env{{ID: "e", Name: "E", TagPrefix: "v"}}}
	tags := []RemoteTag{
		{Tag: "v1.0.0", Digest: "sha256:" + strings.Repeat("a", 64)},
		{Tag: "v1.0.1;iex(calc)"},
		{Tag: "v1.0.2'$(id)'"},
		{Tag: "v1.0.3", Digest: "sha256:not-a-digest"},
	}
	rows, unmatched := matchTags(p, "app", tags, nil)
	got := []string{}
	for _, r := range rows {
		got = append(got, r.Tag)
	}
	if strings.Join(got, ",") != "v1.0.0,v1.0.3" || len(unmatched) != 0 {
		t.Errorf("rows = %v, unmatched = %v — tag lạ phải bị bỏ hẳn", got, unmatched)
	}
	if rows[1].Digest != "" {
		t.Errorf("digest không phải sha256 phải bị bỏ, đang là %q", rows[1].Digest)
	}
}

func TestCheckVersionFollowsDockerTagGrammar(t *testing.T) {
	for _, ok := range []string{"1.2.3", "v1.0.1-fix-1.1", "20261019-1437", "release_2"} {
		if _, msg := checkVersion(ok); msg != "" {
			t.Errorf("%q bị từ chối nhầm: %s", ok, msg)
		}
	}
	for _, bad := range []string{"1.0;calc", "1.0'x'", "$(id)", "-rf", ".hidden", "a b", "1:2", strings.Repeat("9", 129)} {
		if _, msg := checkVersion(bad); msg == "" {
			t.Errorf("%q phải bị từ chối", bad)
		}
	}
}

// Thay biến một lượt: giá trị đã thay vào không bị đọc lại như một biến.
func TestExpandVarsIsSinglePass(t *testing.T) {
	got := expandVars("{labels} -t {ref}", map[string]string{
		"labels": "--label 'dima.branch=fix{ref}'", "ref": "app:1.0",
	})
	if got != "--label 'dima.branch=fix{ref}' -t app:1.0" {
		t.Errorf("= %q — {ref} trong tên nhánh không được thay lần nữa", got)
	}
}

func TestSameSiteRejectsIPsAndPublicSuffixes(t *testing.T) {
	for _, tc := range []struct {
		realm, registry string
		want            bool
	}{
		{"10.0.0.5", "10.0.0.5:5000", true},
		{"192.168.0.5", "10.0.0.5:5000", false},
		{"evil.co.uk", "registry.a.co.uk", false},
		{"gitlab.com", "registry.gitlab.com", true},
		{"auth.harbor.tech", "harbor.tech", true},
		{"com", "harbor.com", false},
	} {
		if got := sameSite(tc.realm, tc.registry); got != tc.want {
			t.Errorf("sameSite(%s, %s) = %v, muốn %v", tc.realm, tc.registry, got, tc.want)
		}
	}
}

// Realm đã có sẵn query thì service/scope phải được ghép vào, không nối sau
// một dấu ? thứ hai.
func TestTokenKeepsTheRealmsOwnQuery(t *testing.T) {
	var got string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.RawQuery
		json.NewEncoder(w).Encode(map[string]string{"token": "t"})
	}))
	defer srv.Close()
	c := clientFor(srv, "", "")
	if _, err := c.token(hostOf(srv), map[string]string{
		"realm": srv.URL + "/token?account=x", "service": "reg", "scope": "repository:a:pull",
	}, "", ""); err != nil {
		t.Fatal(err)
	}
	if got != "account=x&scope=repository%3Aa%3Apull&service=reg" {
		t.Errorf("query = %q", got)
	}
}

func TestNextLink(t *testing.T) {
	if got := nextLink(`</v2/a/tags/list?last=x&n=1000>; rel="next"`); got != "/v2/a/tags/list?last=x&n=1000" {
		t.Errorf("= %q", got)
	}
	if got := nextLink(`<https://evil.example/v2/a/tags/list>; rel="next"`); got != "" {
		t.Errorf("link sang host khác không được theo: %q", got)
	}
	if got := nextLink(""); got != "" {
		t.Errorf("= %q", got)
	}
}
