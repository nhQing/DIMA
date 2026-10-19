package main

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// ---------- nhận diện tham số trong lệnh ----------

func TestCommandParamsListsOnlyTheUsersOwn(t *testing.T) {
	got := commandParams(`./build.ps1 -Ref {ref} -Cache {cache} -Push {push} -Again {cache} {{.Id}}`)
	want := []string{"cache", "push"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("= %v, muốn %v — biến DIMA tự điền, biến lặp lại và Go template không được tính", got, want)
	}
}

// {version} đã bỏ có chủ ý; để nó quay lại dưới dạng tham số tự khai thì
// lỗi lệch tiền tố tag cũng quay lại theo.
func TestVersionCannotComeBackAsAParam(t *testing.T) {
	if got := commandParams("./b {version}"); len(got) != 0 {
		t.Errorf("= %v, {version} không được thành tham số", got)
	}
	if msg := CheckParams([]Param{{Name: "version"}}); msg == "" {
		t.Error("phải từ chối tham số tên version")
	}
}

func TestCheckParams(t *testing.T) {
	bad := map[string][]Param{
		"tên trùng biến có sẵn": {{Name: "ref"}},
		"tên có dấu cách":       {{Name: "my cache"}},
		"tên bắt đầu bằng số":   {{Name: "1x"}},
		"kiểu lạ":               {{Name: "x", Kind: "number"}},
		"chọn mà không có gì":   {{Name: "x", Kind: ParamChoice, Options: " , "}},
	}
	for why, params := range bad {
		if CheckParams(params) == "" {
			t.Errorf("%s: phải bị từ chối", why)
		}
	}
	ok := []Param{
		{Name: "cache", Kind: ParamFlag, Options: "-NoCache"},
		{Name: "mode", Kind: ParamChoice, Options: "fast, full"},
		{Name: "note"},
		{Name: ""}, // dòng trống bị bỏ qua
	}
	if msg := CheckParams(ok); msg != "" {
		t.Errorf("bị từ chối nhầm: %s", msg)
	}
}

func TestParamsAreInheritedAndOverriddenByName(t *testing.T) {
	p := Project{
		BuildCommand: "./b {ref} {cache} {mode}",
		Params: []Param{
			{Name: "cache", Kind: ParamFlag, Options: "-NoCache"},
			{Name: "mode", Kind: ParamChoice, Options: "fast,full", Default: "fast"},
		},
	}
	env := p.Effective(Env{Params: []Param{{Name: "mode", Kind: ParamChoice, Options: "fast,full", Default: "full"}}})
	if len(env.Params) != 2 {
		t.Fatalf("có %d tham số, muốn 2", len(env.Params))
	}
	if env.Params[1].Default != "full" {
		t.Errorf("môi trường khai riêng mode thì phải thắng, đang là %q", env.Params[1].Default)
	}
	if env.Params[0].Options != "-NoCache" {
		t.Error("cache không bị ghi đè thì phải kế thừa của dự án")
	}
}

// ---------- giá trị khởi đầu và giá trị khi chạy ----------

func TestParamFieldsStartFromGivenThenLastThenDefault(t *testing.T) {
	env := Env{
		BuildCommand: "./b {a} {b} {c} {d}",
		Params: []Param{
			{Name: "a", Default: "mặc-định-a"},
			{Name: "b", Default: "mặc-định-b"},
			{Name: "c", Default: "mặc-định-c"},
		},
		LastParams: map[string]string{"b": "lần-trước-b", "c": "lần-trước-c"},
	}
	fields := paramFields(env, map[string]string{"c": "vừa-gõ"})
	got := map[string]string{}
	for _, f := range fields {
		got[f.Name] = f.Value
	}
	want := map[string]string{"a": "mặc-định-a", "b": "lần-trước-b", "c": "vừa-gõ", "d": ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("= %v, muốn %v", got, want)
	}
	// {d} không có dòng khai báo vẫn là một tham số: ô chữ, không bắt buộc.
	if fields[3].Kind != ParamText || fields[3].Required {
		t.Errorf("tham số chưa khai báo phải là ô chữ không bắt buộc, đang là %+v", fields[3].Param)
	}
}

func TestFlagExpandsToItsTextOnlyWhenTicked(t *testing.T) {
	def := Param{Name: "cache", Kind: ParamFlag, Options: "-NoCache"}
	on, _ := paramValues([]ParamField{{Param: def, Value: "true"}})
	off, _ := paramValues([]ParamField{{Param: def, Value: "false"}})
	if on["cache"] != "-NoCache" {
		t.Errorf("tick thì phải ra -NoCache, đang là %q", on["cache"])
	}
	if off["cache"] != "" {
		t.Errorf("không tick thì phải rỗng, đang là %q", off["cache"])
	}
}

func TestRequiredAndChoiceProblems(t *testing.T) {
	_, problems := paramValues([]ParamField{
		{Param: Param{Name: "token", Required: true}, Value: "  "},
		{Param: Param{Name: "mode", Kind: ParamChoice, Options: "fast,full"}, Value: "slow"},
		{Param: Param{Name: "note"}, Value: ""},
		// Ô tick không có khái niệm "chưa điền", nên bắt buộc không áp vào nó.
		{Param: Param{Name: "push", Kind: ParamFlag, Required: true}, Value: "false"},
	})
	if len(problems) != 2 || problems[0] != "token" || !strings.HasPrefix(problems[1], "mode") {
		t.Errorf("problems = %v, muốn token và mode", problems)
	}
}

// ---------- kế hoạch build ----------

func TestPlanBuildExpandsUserParamsAlongsideBuiltins(t *testing.T) {
	p := Project{
		Name: "admin", Registry: "harbor.tech", Image: "mmt/admin",
		BuildCommand: "./build.ps1 -Ref {ref} {cache} -Mode {mode}",
		Params: []Param{
			{Name: "cache", Kind: ParamFlag, Options: "-NoCache"},
			{Name: "mode", Kind: ParamChoice, Options: "fast,full", Required: true},
		},
	}
	env := Env{ID: "e", Name: "Production", TagPrefix: "prod-v"}
	plan := planBuild("docker", p, env, "1.1.1", map[string]string{"cache": "true", "mode": "full"}, GitInfo{})
	want := "./build.ps1 -Ref harbor.tech/mmt/admin:prod-v1.1.1 -NoCache -Mode full"
	if plan.Command != want {
		t.Errorf("lệnh = %q\nmuốn   %q", plan.Command, want)
	}
	if len(plan.Problems) != 0 {
		t.Errorf("problems = %v, muốn rỗng", plan.Problems)
	}
	if got := plan.remembered(); got["cache"] != "true" || got["mode"] != "full" {
		t.Errorf("giá trị nhớ lại = %v — phải nhớ trạng thái ô, không phải chữ đã thay", got)
	}
}

func TestEmptyParamLeavesNoDoubleSpace(t *testing.T) {
	p := Project{Image: "web", BuildCommand: "./b -Ref {ref} {cache} -Mode {mode}",
		Params: []Param{{Name: "cache", Kind: ParamFlag, Options: "-NoCache"}}}
	plan := planBuild("docker", p, Env{}, "1.0.0", map[string]string{"cache": "false", "mode": "fast"}, GitInfo{})
	if want := "./b -Ref web:1.0.0 -Mode fast"; plan.Command != want {
		t.Errorf("lệnh = %q, muốn %q", plan.Command, want)
	}
}

func TestPlanBuildWithoutCustomCommandIsDockerBuild(t *testing.T) {
	p := Project{Image: "web", Context: `D:\web`}
	plan := planBuild("docker", p, Env{TagPrefix: "s-"}, "2.0.0", nil, GitInfo{})
	if !strings.HasPrefix(plan.Command, "docker build -t web:s-2.0.0") {
		t.Errorf("lệnh = %q", plan.Command)
	}
	if len(plan.Fields) != 0 {
		t.Error("docker build không có tham số tự khai")
	}
}

// ---------- API ----------

func TestPreviewShowsTheCommandAndWhatIsMissing(t *testing.T) {
	store, srv := newTestAPI(t)
	p := Project{
		ID: "p", Name: "admin", Image: "mmt/admin", Context: t.TempDir(),
		BuildCommand: "./build.ps1 -Ref {ref} -Token {token}",
		Params:       []Param{{Name: "token", Required: true}},
		Envs:         []Env{{ID: "e", Name: "Staging", TagPrefix: "staging-"}},
	}
	if err := store.SaveConfig(Config{Projects: []Project{p}}); err != nil {
		t.Fatal(err)
	}

	code, body := postJSON(t, srv.URL+"/api/build/preview", `{"envId":"e","version":"1.0.0","push":true}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %v", code, body)
	}
	if body["command"] != "./build.ps1 -Ref mmt/admin:staging-1.0.0 -Token" {
		t.Errorf("command = %q", body["command"])
	}
	if steps, _ := body["steps"].([]any); len(steps) != 2 || !strings.Contains(steps[1].(string), "push mmt/admin:staging-1.0.0") {
		t.Errorf("steps = %v, muốn thêm bước push", body["steps"])
	}
	if problems, _ := body["problems"].([]any); len(problems) != 1 || problems[0] != "token" {
		t.Errorf("problems = %v, muốn [token]", body["problems"])
	}

	// Build bị từ chối khi tham số bắt buộc còn trống, trước khi chạy gì.
	code, body = postJSON(t, srv.URL+"/api/build", `{"envId":"e","version":"1.0.0"}`)
	if code != http.StatusBadRequest || !strings.Contains(body["error"].(string), "token") {
		t.Errorf("status = %d, body = %v, muốn 400 nêu tên token", code, body)
	}
	if len(store.Builds()) != 0 {
		t.Error("bị từ chối thì không được ghi bản build nào vào lịch sử")
	}
}

// Dự án promote không còn chặn build ở môi trường đích: mọi môi trường build
// được, xem trước chỉ báo là bản này đi ngoài chuỗi promote.
func TestPromoteProjectBuildsInEveryEnv(t *testing.T) {
	store, srv := newTestAPI(t)
	p := Project{
		ID: "p", Name: "web", Image: "web", Context: t.TempDir(),
		BuildCommand: "echo ok", ReleaseMode: ModePromote, SourceEnvID: "stg",
		Envs: []Env{{ID: "stg", Name: "Staging", TagPrefix: "s-"}, {ID: "prd", Name: "Production", TagPrefix: "p-"}},
	}
	if err := store.SaveConfig(Config{Projects: []Project{p}}); err != nil {
		t.Fatal(err)
	}
	_, preview := postJSON(t, srv.URL+"/api/build/preview", `{"envId":"prd","version":"1.0.0"}`)
	if preview["outsideSource"] != true {
		t.Errorf("outsideSource = %v, muốn true cho Production", preview["outsideSource"])
	}
	// Build được — nhưng phải xác nhận, vì trước đây việc này bị chặn hẳn và
	// cấu hình cũ chưa môi trường nào có cờ "quan trọng".
	if preview["needsConfirm"] != true {
		t.Errorf("needsConfirm = %v, build ngoài nguồn phải xác nhận", preview["needsConfirm"])
	}
	code, body := postJSON(t, srv.URL+"/api/build", `{"envId":"prd","version":"1.0.0"}`)
	if code != http.StatusConflict {
		t.Fatalf("status = %d, body = %v, muốn 409 khi chưa xác nhận", code, body)
	}
	code, body = postJSON(t, srv.URL+"/api/build", `{"envId":"prd","version":"1.0.0","confirm":true}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %v, có xác nhận thì phải build được ở Production", code, body)
	}
	waitBuilds(t, store)
}

// Môi trường quan trọng: build thẳng phải có xác nhận, và máy chủ tự kiểm
// chứ không tin vào hộp thoại.
func TestProtectedEnvNeedsConfirmToBuild(t *testing.T) {
	store, srv := newTestAPI(t)
	p := Project{
		ID: "p", Name: "web", Image: "web", Context: t.TempDir(), BuildCommand: "echo ok",
		Envs: []Env{{ID: "stg", Name: "Staging", TagPrefix: "s-"}, {ID: "prd", Name: "Production", TagPrefix: "p-", Protected: true}},
	}
	if err := store.SaveConfig(Config{Projects: []Project{p}}); err != nil {
		t.Fatal(err)
	}
	_, preview := postJSON(t, srv.URL+"/api/build/preview", `{"envId":"prd","version":"1.0.0"}`)
	if preview["needsConfirm"] != true || !strings.Contains(preview["confirmWhy"].(string), "Staging") {
		t.Errorf("preview = %v, muốn needsConfirm và gợi ý build ở Staging", preview)
	}

	code, body := postJSON(t, srv.URL+"/api/build", `{"envId":"prd","version":"1.0.0"}`)
	if code != http.StatusConflict {
		t.Fatalf("status = %d, body = %v, muốn 409 khi chưa xác nhận", code, body)
	}
	if len(store.Builds()) != 0 {
		t.Error("bị từ chối thì không được chạy gì")
	}
	code, _ = postJSON(t, srv.URL+"/api/build", `{"envId":"prd","version":"1.0.0","confirm":true}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, có xác nhận thì phải build được", code)
	}
	// Môi trường thường không cần xác nhận.
	code, _ = postJSON(t, srv.URL+"/api/build", `{"envId":"stg","version":"1.0.0"}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, môi trường thường không cần xác nhận", code)
	}
	waitBuilds(t, store)
}

func TestSaveConfigRefusesBadParams(t *testing.T) {
	_, srv := newTestAPI(t)
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/config", strings.NewReader(
		`{"projects":[{"name":"web","image":"web","params":[{"name":"ref"}],"envs":[]}]}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, muốn 400 khi tham số trùng tên biến có sẵn", resp.StatusCode)
	}
}
