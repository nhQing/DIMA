package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func kvMap(rows []KV) map[string]string {
	m := map[string]string{}
	for _, r := range rows {
		m[r.Key] = r.Value
	}
	return m
}

func TestEffectiveInheritsEmptyFieldsFromProject(t *testing.T) {
	p := Project{
		Registry: "reg.local", Image: "my-api", Context: "/src",
		Dockerfile: "Dockerfile", Platform: "linux/amd64", Target: "runtime",
	}
	e := Env{Name: "Staging"}

	got := p.Effective(e)

	for _, c := range []struct{ name, got, want string }{
		{"registry", got.Registry, "reg.local"},
		{"image", got.Image, "my-api"},
		{"context", got.Context, "/src"},
		{"dockerfile", got.Dockerfile, "Dockerfile"},
		{"platform", got.Platform, "linux/amd64"},
		{"target", got.Target, "runtime"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, muốn %q", c.name, c.got, c.want)
		}
	}
}

func TestEffectiveEnvOverridesProject(t *testing.T) {
	p := Project{Registry: "reg.local", Image: "my-api", Dockerfile: "Dockerfile"}
	e := Env{Dockerfile: "Dockerfile.prod"}

	got := p.Effective(e)

	if got.Dockerfile != "Dockerfile.prod" {
		t.Errorf("dockerfile = %q, muốn bản ghi đè của env", got.Dockerfile)
	}
	if got.Registry != "reg.local" {
		t.Errorf("registry = %q, muốn vẫn kế thừa từ project", got.Registry)
	}
}

func TestEffectiveMergesBuildArgsByKey(t *testing.T) {
	p := Project{BuildArgs: []KV{{Key: "APP", Value: "api"}, {Key: "NODE_ENV", Value: "staging"}}}
	e := Env{BuildArgs: []KV{{Key: "NODE_ENV", Value: "production"}, {Key: "REPLICAS", Value: "3"}}}

	got := kvMap(p.Effective(e).BuildArgs)

	if got["APP"] != "api" {
		t.Errorf("APP = %q, muốn giá trị của project được giữ", got["APP"])
	}
	if got["NODE_ENV"] != "production" {
		t.Errorf("NODE_ENV = %q, muốn env thắng khi trùng key", got["NODE_ENV"])
	}
	if got["REPLICAS"] != "3" {
		t.Errorf("REPLICAS = %q, muốn key riêng của env được thêm vào", got["REPLICAS"])
	}
	if len(got) != 3 {
		t.Errorf("có %d build arg, muốn 3", len(got))
	}
}

func TestEffectiveDropsRowsWithEmptyKey(t *testing.T) {
	p := Project{BuildArgs: []KV{{Key: "", Value: "rác"}}}
	e := Env{BuildArgs: []KV{{Key: "  ", Value: "rác"}, {Key: "OK", Value: "1"}}}

	got := p.Effective(e).BuildArgs

	if len(got) != 1 || got[0].Key != "OK" {
		t.Errorf("build args = %+v, muốn chỉ còn dòng có key", got)
	}
}

func TestEffectiveConcatenatesExtraFlags(t *testing.T) {
	p := Project{ExtraFlags: "--no-cache"}
	e := Env{ExtraFlags: "--pull"}

	if got := p.Effective(e).ExtraFlags; got != "--no-cache --pull" {
		t.Errorf("extraFlags = %q, muốn nối cả hai", got)
	}
}

func TestEffectiveExtraFlagsWhenOnlyOneSideSet(t *testing.T) {
	if got := (Project{ExtraFlags: "--no-cache"}).Effective(Env{}).ExtraFlags; got != "--no-cache" {
		t.Errorf("chỉ project có cờ: %q", got)
	}
	if got := (Project{}).Effective(Env{ExtraFlags: "--pull"}).ExtraFlags; got != "--pull" {
		t.Errorf("chỉ env có cờ: %q", got)
	}
	if got := (Project{}).Effective(Env{}).ExtraFlags; got != "" {
		t.Errorf("không bên nào có cờ: %q, muốn rỗng", got)
	}
}

func TestTagAppliesPrefix(t *testing.T) {
	e := Env{TagPrefix: "prod-"}
	if got := e.Tag("1.2.3"); got != "prod-1.2.3" {
		t.Errorf("Tag = %q, muốn prod-1.2.3", got)
	}
	if got := (Env{}).Tag("1.2.3"); got != "1.2.3" {
		t.Errorf("không prefix: Tag = %q", got)
	}
	if got := e.Tag(""); got != "prod-latest" {
		t.Errorf("version rỗng: Tag = %q, muốn prod-latest", got)
	}
}

func TestRefAndRepo(t *testing.T) {
	e := Env{Registry: "reg.local/", Image: "/team/my-api", TagPrefix: "staging-"}
	if got := e.Repo(); got != "reg.local/team/my-api" {
		t.Errorf("Repo = %q", got)
	}
	if got := e.Ref("1.0.0"); got != "reg.local/team/my-api:staging-1.0.0" {
		t.Errorf("Ref = %q", got)
	}
	if got := (Env{Image: "my-api"}).Ref("1.0.0"); got != "my-api:1.0.0" {
		t.Errorf("không registry: Ref = %q", got)
	}
}

func TestCanBuildRebuildModeAllowsEveryEnv(t *testing.T) {
	p := Project{ReleaseMode: ModeRebuild, Envs: []Env{{ID: "a"}, {ID: "b"}}}
	if !p.CanBuild("a") || !p.CanBuild("b") {
		t.Error("chế độ rebuild phải cho build ở mọi môi trường")
	}
}

func TestCanBuildPromoteModeAllowsOnlySourceEnv(t *testing.T) {
	p := Project{ReleaseMode: ModePromote, SourceEnvID: "a", Envs: []Env{{ID: "a"}, {ID: "b"}}}
	if !p.CanBuild("a") {
		t.Error("env nguồn phải build được")
	}
	if p.CanBuild("b") {
		t.Error("env đích chỉ được nhận promote, không được build")
	}
}

func TestCanBuildPromoteFallsBackToFirstEnv(t *testing.T) {
	p := Project{ReleaseMode: ModePromote, SourceEnvID: "không-tồn-tại", Envs: []Env{{ID: "a"}, {ID: "b"}}}
	if !p.CanBuild("a") {
		t.Error("nguồn trỏ sai phải lùi về env đầu tiên")
	}
	if p.CanBuild("b") {
		t.Error("env thứ hai vẫn không được build")
	}
}

// ---------- migration ----------

const v1Config = `{
  "schemaVersion": 1,
  "dockerBin": "docker",
  "environments": [
    {"id":"e1","name":"Staging","registry":"reg.local","image":"my-api","context":".","dockerfile":"Dockerfile",
     "buildArgs":[{"key":"NODE_ENV","value":"staging"}],"labels":[],"lastTag":"1.2.3"},
    {"id":"e2","name":"Production","registry":"reg.local","image":"my-api","context":".","dockerfile":"Dockerfile.prod",
     "buildArgs":[{"key":"NODE_ENV","value":"production"}],"labels":[],"lastTag":"1.2.2"}
  ]
}`

func TestMigrateV1WrapsEnvsInOneProject(t *testing.T) {
	c, migrated, err := decodeConfig([]byte(v1Config))
	if err != nil {
		t.Fatal(err)
	}
	if !migrated {
		t.Fatal("đọc file v1 phải báo là đã migrate")
	}
	if len(c.Projects) != 1 {
		t.Fatalf("có %d project, muốn 1", len(c.Projects))
	}
	if n := len(c.Projects[0].Envs); n != 2 {
		t.Fatalf("project có %d env, muốn 2", n)
	}
	if c.SchemaVersion != currentSchema {
		t.Errorf("schemaVersion = %d, muốn %d", c.SchemaVersion, currentSchema)
	}
	if c.Projects[0].ReleaseMode != ModeRebuild {
		t.Errorf("releaseMode = %q, muốn giữ hành vi cũ là rebuild", c.Projects[0].ReleaseMode)
	}
}

func TestMigrateV1LiftsSharedSettingsButNotDifferingOnes(t *testing.T) {
	c, _, err := decodeConfig([]byte(v1Config))
	if err != nil {
		t.Fatal(err)
	}
	p := c.Projects[0]

	if p.Registry != "reg.local" {
		t.Errorf("registry của project = %q, muốn được nâng lên", p.Registry)
	}
	if p.Image != "my-api" {
		t.Errorf("image của project = %q, muốn được nâng lên", p.Image)
	}
	if p.Envs[0].Registry != "" {
		t.Errorf("registry của env = %q, muốn bị xóa sau khi nâng lên", p.Envs[0].Registry)
	}
	// Dockerfile khác nhau giữa hai env nên không được nâng lên.
	if p.Dockerfile != "" {
		t.Errorf("dockerfile của project = %q, muốn để trống vì hai env khác nhau", p.Dockerfile)
	}
	if p.Envs[0].Dockerfile != "Dockerfile" || p.Envs[1].Dockerfile != "Dockerfile.prod" {
		t.Errorf("dockerfile riêng của env bị mất: %q / %q", p.Envs[0].Dockerfile, p.Envs[1].Dockerfile)
	}
}

func TestMigrateV1KeepsEffectiveValuesIdentical(t *testing.T) {
	c, _, err := decodeConfig([]byte(v1Config))
	if err != nil {
		t.Fatal(err)
	}
	p := c.Projects[0]

	staging := p.Effective(p.Envs[0])
	if staging.Registry != "reg.local" || staging.Image != "my-api" ||
		staging.Context != "." || staging.Dockerfile != "Dockerfile" {
		t.Errorf("giá trị hiệu lực của staging đã đổi sau migration: %+v", staging)
	}
	prod := p.Effective(p.Envs[1])
	if prod.Dockerfile != "Dockerfile.prod" {
		t.Errorf("dockerfile hiệu lực của production = %q", prod.Dockerfile)
	}
	if kvMap(prod.BuildArgs)["NODE_ENV"] != "production" {
		t.Errorf("build args của production bị mất: %+v", prod.BuildArgs)
	}
}

func TestMigrateV1MovesLastTagToLastVersion(t *testing.T) {
	c, _, err := decodeConfig([]byte(v1Config))
	if err != nil {
		t.Fatal(err)
	}
	envs := c.Projects[0].Envs
	if envs[0].LastVersion != "1.2.3" {
		t.Errorf("lastVersion = %q, muốn lấy từ lastTag cũ", envs[0].LastVersion)
	}
	if envs[0].LastTag != "" {
		t.Errorf("lastTag = %q, muốn bị xóa sau migration", envs[0].LastTag)
	}
}

// Đổi tên từ TestDecodeV2ConfigIsNotMigrated khi lên schema 3: test này
// luôn dựng file từ defaultConfig(), tức là schema hiện tại chứ không phải
// v2. Test migration v2 thật nằm ở store_schema3_test.go.
func TestDecodeCurrentSchemaConfigIsNotMigrated(t *testing.T) {
	raw, err := json.Marshal(defaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	_, migrated, err := decodeConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if migrated {
		t.Error("file đã đúng schema hiện tại thì không được báo migrate")
	}
}

func TestDecodeRejectsBrokenJSON(t *testing.T) {
	if _, _, err := decodeConfig([]byte("{không phải json")); err == nil {
		t.Error("JSON hỏng phải trả lỗi")
	}
}

func TestNormaliseFillsMissingIDsAndDefaults(t *testing.T) {
	c := normalise(Config{Projects: []Project{{Name: "P", Envs: []Env{{Name: "E"}}}}})

	p := c.Projects[0]
	if p.ID == "" || p.Envs[0].ID == "" {
		t.Error("project và env phải được cấp id")
	}
	if c.DockerBin != "docker" {
		t.Errorf("dockerBin = %q, muốn mặc định docker", c.DockerBin)
	}
	if p.ReleaseMode != ModeRebuild {
		t.Errorf("releaseMode = %q, muốn mặc định rebuild", p.ReleaseMode)
	}
	if p.BuildArgs == nil || p.Envs[0].Labels == nil {
		t.Error("các slice phải khác nil để JSON ra [] chứ không phải null")
	}
}

func TestNormalisePointsPromoteProjectAtARealEnv(t *testing.T) {
	c := normalise(Config{Projects: []Project{{
		Name: "P", ReleaseMode: ModePromote, SourceEnvID: "mất-rồi",
		Envs: []Env{{ID: "a"}, {ID: "b"}},
	}}})

	if got := c.Projects[0].SourceEnvID; got != "a" {
		t.Errorf("sourceEnvId = %q, muốn tự sửa về env đầu tiên", got)
	}
}

// ---------- store on disk ----------

func TestOpenStoreMigratesFileAndKeepsBackup(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "versions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(v1Config), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Config().Projects) != 1 {
		t.Fatal("config trên đĩa chưa được migrate")
	}

	// Bản v1 gốc phải còn để khôi phục được.
	if len(s.Snapshots()) != 1 {
		t.Errorf("có %d bản lưu, muốn 1 bản của file v1", len(s.Snapshots()))
	}

	// Mở lại lần nữa không được migrate thêm lần nào.
	s2, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(s2.Snapshots()); n != 1 {
		t.Errorf("mở lần hai tạo thêm bản lưu: có %d, muốn 1", n)
	}
}

func TestOpenStoreBackfillsBuildHistory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "versions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(v1Config), 0o644); err != nil {
		t.Fatal(err)
	}
	oldHistory := `{"builds":[{"id":"b1","envId":"e1","envName":"Staging","tag":"1.2.3",
	  "ref":"reg.local/my-api:1.2.3","status":"success","pushed":true,"startedAt":"2024-01-01T10:00:00Z"}]}`
	if err := os.WriteFile(filepath.Join(root, "history.json"), []byte(oldHistory), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	b := s.Builds()[0]
	if b.Version != "1.2.3" {
		t.Errorf("version = %q, muốn lấy từ tag cũ", b.Version)
	}
	if b.Kind != KindBuild {
		t.Errorf("kind = %q, muốn %q", b.Kind, KindBuild)
	}
	if b.ProjectID == "" || b.ProjectName == "" {
		t.Error("bản build cũ phải được gắn vào project sau migration")
	}
	if b.ProjectID != s.Config().Projects[0].ID {
		t.Error("bản build được gắn sai project")
	}
}

func TestRunningBuildFromAPreviousSessionIsNotStillRunning(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "versions"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := `{"builds":[{"id":"b1","envId":"e1","status":"running","startedAt":"2024-01-01T10:00:00Z"}]}`
	if err := os.WriteFile(filepath.Join(root, "history.json"), []byte(h), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Builds()[0].Status; got != "canceled" {
		t.Errorf("status = %q, muốn canceled", got)
	}
}

func TestSnapshotsIgnoreHistoryBackups(t *testing.T) {
	root := t.TempDir()
	s, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "versions")
	if err := os.WriteFile(filepath.Join(dir, "history-20240101-000000.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config-20240101-000000.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	snaps := s.Snapshots()
	if len(snaps) != 1 || snaps[0].File != "config-20240101-000000.json" {
		t.Errorf("Snapshots = %+v, muốn chỉ có bản lưu của config", snaps)
	}
}

func TestRememberVersionWritesThrough(t *testing.T) {
	root := t.TempDir()
	s, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	envID := s.Config().Projects[0].Envs[0].ID

	s.rememberVersion(envID, "9.9.9")

	reopened, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	_, e, ok := reopened.Config().FindEnv(envID)
	if !ok {
		t.Fatal("không tìm thấy env sau khi mở lại")
	}
	if e.LastVersion != "9.9.9" {
		t.Errorf("lastVersion = %q, muốn 9.9.9 sau khi mở lại", e.LastVersion)
	}
}

func TestFindEnvLocatesOwningProject(t *testing.T) {
	c := normalise(Config{Projects: []Project{
		{Name: "A", Envs: []Env{{ID: "a1"}}},
		{Name: "B", Envs: []Env{{ID: "b1"}}},
	}})

	p, e, ok := c.FindEnv("b1")
	if !ok || p.Name != "B" || e.ID != "b1" {
		t.Errorf("FindEnv(b1) = %q/%q/%v", p.Name, e.ID, ok)
	}
	if _, _, ok := c.FindEnv("không có"); ok {
		t.Error("FindEnv với id lạ phải trả false")
	}
}

func TestDecodeToleratesUTF8BOM(t *testing.T) {
	raw := append([]byte("\xef\xbb\xbf"), []byte(v1Config)...)

	c, migrated, err := decodeConfig(raw)
	if err != nil {
		t.Fatalf("file có BOM phải đọc được: %v", err)
	}
	if !migrated || len(c.Projects) != 1 {
		t.Errorf("migrated=%v, số project=%d", migrated, len(c.Projects))
	}
}
