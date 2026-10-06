package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// v2Config is a realistic schema 2 file: inheritance in both directions, a
// rebuild project and a promote one, versions already shipped.
const v2Config = `{
  "schemaVersion": 2,
  "dockerBin": "docker",
  "projects": [
    {
      "id": "p1", "name": "API", "group": "khach-A", "releaseMode": "rebuild",
      "registry": "reg.local", "image": "team/api", "context": "D:\\work\\khach-A\\api",
      "dockerfile": "Dockerfile", "platform": "linux/amd64", "extraFlags": "--no-cache",
      "buildArgs": [{"key": "APP", "value": "api"}],
      "labels": [{"key": "owner", "value": "qing"}],
      "envs": [
        {"id": "e1", "name": "Staging", "tagPrefix": "staging-",
         "buildArgs": [{"key": "NODE_ENV", "value": "staging"}], "lastVersion": "1.2.3"},
        {"id": "e2", "name": "Production", "tagPrefix": "prod-",
         "dockerfile": "Dockerfile.prod", "extraFlags": "--pull", "autoPush": true,
         "buildArgs": [{"key": "NODE_ENV", "value": "production"}], "lastVersion": "1.2.2"}
      ]
    },
    {
      "id": "p2", "name": "Web", "releaseMode": "promote", "sourceEnvId": "e4",
      "registry": "harbor.tech", "image": "mm/web", "dockerfile": "Dockerfile",
      "envs": [
        {"id": "e3", "name": "Staging", "tagPrefix": "staging-", "lastVersion": "0.9.0"},
        {"id": "e4", "name": "Production", "tagPrefix": "prod-", "lastVersion": "0.8.7"}
      ]
    }
  ]
}`

// ---------- migration v2 -> v3 ----------

func TestMigrateV2KeepsEveryEffectiveValueIdentical(t *testing.T) {
	// Đọc thẳng vào Config, không qua migration: đây là "trước".
	var before Config
	if err := json.Unmarshal([]byte(v2Config), &before); err != nil {
		t.Fatal(err)
	}

	after, migrated, err := decodeConfig([]byte(v2Config))
	if err != nil {
		t.Fatal(err)
	}
	if !migrated {
		t.Fatal("file v2 phải được báo là đã migrate")
	}
	if after.SchemaVersion != currentSchema {
		t.Fatalf("schemaVersion = %d, muốn %d", after.SchemaVersion, currentSchema)
	}
	if len(after.Projects) != len(before.Projects) {
		t.Fatalf("có %d project sau migration, muốn %d", len(after.Projects), len(before.Projects))
	}

	for i := range before.Projects {
		bp, ap := before.Projects[i], after.Projects[i]
		if len(ap.Envs) != len(bp.Envs) {
			t.Fatalf("project %q: có %d env sau migration, muốn %d", bp.Name, len(ap.Envs), len(bp.Envs))
		}
		for j := range bp.Envs {
			want := bp.Effective(bp.Envs[j])
			got := ap.Effective(ap.Envs[j])
			if !reflect.DeepEqual(got, want) {
				t.Errorf("env %q của %q đổi giá trị hiệu lực:\nsau  = %+v\ntrước = %+v",
					bp.Envs[j].Name, bp.Name, got, want)
			}
			if got.Ref("1.2.3") != want.Ref("1.2.3") {
				t.Errorf("ref của %q: %q, muốn %q", bp.Envs[j].Name, got.Ref("1.2.3"), want.Ref("1.2.3"))
			}
		}
	}
}

func TestMigrateV2IsPurelyAdditive(t *testing.T) {
	c, _, err := decodeConfig([]byte(v2Config))
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range c.Projects {
		if p.Origin != OriginUser {
			t.Errorf("project %q: origin = %q, muốn %q", p.Name, p.Origin, OriginUser)
		}
		if p.FolderPath != "" || p.FolderMissing || p.IsOnlyLocal {
			t.Errorf("project %q: các cờ mới phải để trống, đang là %q/%v/%v",
				p.Name, p.FolderPath, p.FolderMissing, p.IsOnlyLocal)
		}
	}
	if c.FolderGroups == nil {
		t.Error("folderGroups phải khác nil để JSON ra [] chứ không phải null")
	}
	if len(c.FolderGroups) != 0 {
		t.Errorf("folderGroups = %+v, muốn rỗng: v2 chưa có thư mục nào được theo dõi", c.FolderGroups)
	}
	// Những thứ v2 đã có không được đụng tới.
	if c.Projects[0].Group != "khach-A" {
		t.Errorf("group = %q, muốn giữ nguyên", c.Projects[0].Group)
	}
	if c.Projects[1].SourceEnvID != "e4" {
		t.Errorf("sourceEnvId = %q, muốn giữ nguyên e4", c.Projects[1].SourceEnvID)
	}
	if c.Projects[0].Envs[0].LastVersion != "1.2.3" {
		t.Errorf("lastVersion = %q, muốn giữ nguyên", c.Projects[0].Envs[0].LastVersion)
	}
}

func TestMigrateV2SetsOriginOnlyWhereItIsMissing(t *testing.T) {
	c := migrateV2(Config{Projects: []Project{
		{Name: "cũ"},
		{Name: "do quét thư mục", Origin: OriginFolderSync, FolderPath: "D:\\work\\api"},
	}})

	if c.Projects[0].Origin != OriginUser {
		t.Errorf("project cũ: origin = %q, muốn %q", c.Projects[0].Origin, OriginUser)
	}
	if c.Projects[1].Origin != OriginFolderSync {
		t.Errorf("project của folder-sync bị ghi đè origin: %q", c.Projects[1].Origin)
	}
}

func TestMigrateV1AlsoLandsOnSchema3(t *testing.T) {
	// File v1 nhảy thẳng lên v3, không dừng ở v2: migrateV1 không biết gì về
	// các trường mới nên normalise phải điền hộ.
	c, _, err := decodeConfig([]byte(v1Config))
	if err != nil {
		t.Fatal(err)
	}
	if c.SchemaVersion != currentSchema {
		t.Errorf("schemaVersion = %d, muốn %d", c.SchemaVersion, currentSchema)
	}
	if c.Projects[0].Origin != OriginUser {
		t.Errorf("origin = %q, muốn %q", c.Projects[0].Origin, OriginUser)
	}
	if c.FolderGroups == nil {
		t.Error("folderGroups phải khác nil")
	}
}

func TestOpenStoreMigratesV2FileAndKeepsTheV2Copy(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(v2Config), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Config().SchemaVersion; got != currentSchema {
		t.Fatalf("schemaVersion trong bộ nhớ = %d, muốn %d", got, currentSchema)
	}

	// File trên đĩa phải đã là v3.
	raw, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk Config
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk.SchemaVersion != currentSchema {
		t.Errorf("schemaVersion trên đĩa = %d, muốn %d", onDisk.SchemaVersion, currentSchema)
	}

	// Đường lùi: bản v2 gốc còn nguyên trong versions/.
	snaps := s.Snapshots()
	if len(snaps) != 1 {
		t.Fatalf("có %d bản lưu, muốn 1 bản của file v2", len(snaps))
	}
	backup, err := s.ReadSnapshot(snaps[0].File)
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != v2Config {
		t.Error("bản lưu không khớp nội dung file v2 ban đầu")
	}

	// Mở lại không được migrate thêm lần nữa.
	s2, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(s2.Snapshots()); n != 1 {
		t.Errorf("mở lần hai tạo thêm bản lưu: có %d, muốn 1", n)
	}
}

func TestRestoreSnapshotOfAV2ConfigStillWorks(t *testing.T) {
	root := t.TempDir()
	s, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	writeVersionsFile(t, root, "config-20240101-000000.json", v2Config)

	c, err := s.RestoreSnapshot("config-20240101-000000.json")
	if err != nil {
		t.Fatalf("khôi phục bản lưu v2 phải chạy được: %v", err)
	}
	if c.SchemaVersion != currentSchema {
		t.Errorf("schemaVersion sau khôi phục = %d, muốn %d", c.SchemaVersion, currentSchema)
	}
	if len(c.Projects) != 2 || c.Projects[0].Name != "API" {
		t.Fatalf("nội dung sau khôi phục = %+v", c.Projects)
	}
	if c.Projects[0].Origin != OriginUser {
		t.Errorf("origin sau khôi phục = %q, muốn %q", c.Projects[0].Origin, OriginUser)
	}
	staging := c.Projects[0].Effective(c.Projects[0].Envs[0])
	if got := staging.Ref("1.2.3"); got != "reg.local/team/api:staging-1.2.3" {
		t.Errorf("ref sau khôi phục = %q", got)
	}
}

// ---------- local-only ----------

// localOnlyWithThreeEnvs là dự án đã chạy một thời gian rồi mới bị tick cờ
// "chỉ local": ba môi trường, mỗi cái có tiền tố tag và version đã ship.
func localOnlyWithThreeEnvs() Project {
	return Project{
		ID: "p-local", Name: "Công cụ nội bộ", Image: "tool",
		Envs: []Env{
			{ID: "l1", Name: "Dev", TagPrefix: "dev-", LastVersion: "1.0.0", IsOnlyLocal: true},
			{ID: "l2", Name: "Staging", TagPrefix: "staging-", LastVersion: "1.0.1", IsOnlyLocal: true},
			{ID: "l3", Name: "Production", TagPrefix: "prod-", LastVersion: "1.0.2", IsOnlyLocal: true},
		},
	}
}

func TestNormaliseKeepsEveryEnvOfALocalOnlyProject(t *testing.T) {
	before := localOnlyWithThreeEnvs()

	c := normalise(Config{Projects: []Project{localOnlyWithThreeEnvs()}})

	p := c.Projects[0]
	if len(p.Envs) != 3 {
		t.Fatalf("còn %d môi trường, muốn giữ đủ 3 — ép về một cái sẽ làm mồ côi lịch sử build", len(p.Envs))
	}
	for i, want := range before.Envs {
		got := p.Envs[i]
		if got.ID != want.ID || got.Name != want.Name {
			t.Errorf("môi trường %d = %q/%q, muốn %q/%q", i, got.ID, got.Name, want.ID, want.Name)
		}
		if got.TagPrefix != want.TagPrefix {
			t.Errorf("môi trường %q: tagPrefix = %q, muốn giữ nguyên %q — đổi prefix làm tag mới lệch khỏi tag đã ghi",
				want.Name, got.TagPrefix, want.TagPrefix)
		}
		if got.LastVersion != want.LastVersion {
			t.Errorf("môi trường %q: lastVersion = %q, muốn %q", want.Name, got.LastVersion, want.LastVersion)
		}
	}
	for _, e := range p.Envs {
		if !e.IsOnlyLocal {
			t.Errorf("môi trường %q mất cờ isOnlyLocal", e.Name)
		}
	}
}

func TestLocalOnlyProjectKeepsItsBuildHistoryReachable(t *testing.T) {
	root := t.TempDir()
	s, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveConfig(Config{Projects: []Project{localOnlyWithThreeEnvs()}}); err != nil {
		t.Fatal(err)
	}
	for _, envID := range []string{"l1", "l2", "l3"} {
		s.addBuild(Build{
			ID: "b-" + envID, ProjectID: "p-local", EnvID: envID,
			Version: "1.0.0", Kind: KindBuild, Status: "success",
			StartedAt: time.Now(),
		})
	}

	reopened, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.Config()
	for _, b := range reopened.Builds() {
		if _, _, ok := got.FindEnv(b.EnvID); !ok {
			t.Errorf("build %q trỏ vào môi trường %q không còn tồn tại — lịch sử bị mồ côi", b.ID, b.EnvID)
		}
	}
}

func TestNormaliseDoesNotInventEnvs(t *testing.T) {
	c := normalise(Config{Projects: []Project{{Name: "Chưa khai gì"}}})

	if n := len(c.Projects[0].Envs); n != 0 {
		t.Errorf("có %d môi trường, muốn 0 — normalise không tự đẻ môi trường cho ai cả", n)
	}
}

// ---------- v3 -> v4: cờ local-only chuyển từ dự án xuống môi trường ----------

func TestMigrateV3MovesTheLocalOnlyFlagOntoEveryEnv(t *testing.T) {
	c := migrateV3(Config{SchemaVersion: 3, Projects: []Project{{
		ID: "p", Name: "Công cụ", Image: "tool", IsOnlyLocal: true,
		Envs: []Env{
			{ID: "a", Name: "Dev"},
			{ID: "b", Name: "Prod", TagPrefix: "prod-"},
		},
	}}})

	p := c.Projects[0]
	if p.IsOnlyLocal {
		t.Error("cờ cũ ở cấp dự án phải được xóa sau khi chuyển xuống")
	}
	for _, e := range p.Envs {
		if !e.IsOnlyLocal {
			t.Errorf("môi trường %q không nhận được cờ — bản cũ sẽ bỗng dưng push được", e.Name)
		}
	}
	// Ý nghĩa cũ phải giữ nguyên: trước v4 cờ áp cho mọi thứ dự án build ra,
	// nên đặt lên từng môi trường là nói đúng y như vậy.
	if len(p.Envs) != 2 {
		t.Errorf("còn %d môi trường, migration không được thêm bớt", len(p.Envs))
	}
}

func TestMigrateV3LeavesOrdinaryProjectsAlone(t *testing.T) {
	c := migrateV3(Config{SchemaVersion: 3, Projects: []Project{{
		ID: "p", Name: "web", Image: "mm/web",
		Envs: []Env{{ID: "a", Name: "Staging", TagPrefix: "staging-"}},
	}}})

	for _, e := range c.Projects[0].Envs {
		if e.IsOnlyLocal {
			t.Errorf("môi trường %q bị đánh dấu local-only mà đáng ra không", e.Name)
		}
	}
}

// Một dự án promote không được có môi trường nguồn là local-only: promote
// lấy image từ registry, mà môi trường ấy thì không bao giờ đẩy lên.
func TestPromoteSourceNeverLandsOnALocalOnlyEnv(t *testing.T) {
	c := normalise(Config{Projects: []Project{{
		ID: "p", Name: "builder", Image: "tool", ReleaseMode: ModePromote,
		Envs: []Env{
			{ID: "cache", Name: "Cache", IsOnlyLocal: true},
			{ID: "stg", Name: "Staging", TagPrefix: "staging-"},
		},
	}}})

	p := c.Projects[0]
	if p.SourceEnvID != "stg" {
		t.Errorf("môi trường nguồn = %q, muốn stg — không được rơi vào Cache", p.SourceEnvID)
	}
	// Và Cache vẫn phải build được: nó đứng ngoài chuỗi promote, nên cấm
	// build ở đó là làm nó rỗng vĩnh viễn.
	if !p.CanBuild("cache") {
		t.Error("môi trường local-only phải tự build được dù dự án chạy kiểu promote")
	}
}

// ---------- folder rules ----------

func TestFolderRulesAreKeyedByPathNotByGroupName(t *testing.T) {
	c := normalise(Config{
		FolderGroups: []FolderRule{
			{Path: "D:\\work\\khach-A", Group: "khach-A", Registry: "reg.local"},
			{Path: "D:\\work\\khach-B", Group: "khach-A"},
		},
		Projects: []Project{{
			Name: "API", Origin: OriginFolderSync, FolderPath: "D:\\work\\khach-A\\api",
			// User đã đổi tên nhóm ở sidebar; rule vẫn giữ tên cũ.
			Group: "Khách A (đã đổi tên)",
		}},
	})

	if len(c.FolderGroups) != 2 {
		t.Fatalf("có %d rule, muốn 2 — hai thư mục khác nhau trùng tên nhóm vẫn là hai rule", len(c.FolderGroups))
	}
	if c.Projects[0].Group != "Khách A (đã đổi tên)" {
		t.Errorf("group của dự án = %q, muốn giữ tên user đặt", c.Projects[0].Group)
	}
	if c.FolderGroups[0].Group != "khach-A" {
		t.Errorf("group trên rule = %q — rule không được đi theo tên nhóm user đổi", c.FolderGroups[0].Group)
	}
	if c.Projects[0].Origin != OriginFolderSync {
		t.Errorf("origin = %q, muốn giữ %q", c.Projects[0].Origin, OriginFolderSync)
	}
}

func TestNormaliseCleansFolderRules(t *testing.T) {
	c := normalise(Config{FolderGroups: []FolderRule{
		{Path: "  D:\\work\\api  ", Group: "  khach-A  "},
		{Path: "d:\\work\\api\\", Group: "trùng thư mục"},
		{Path: "   "},
	}})

	if len(c.FolderGroups) != 1 {
		t.Fatalf("có %d rule, muốn 1: rule rỗng bị bỏ, rule trùng đường dẫn bị bỏ — %+v", len(c.FolderGroups), c.FolderGroups)
	}
	r := c.FolderGroups[0]
	if r.Path != "D:\\work\\api" || r.Group != "khach-A" {
		t.Errorf("rule = %q/%q, muốn được trim", r.Path, r.Group)
	}
	if r.ID == "" {
		t.Error("rule phải được cấp id")
	}
}

func TestFolderKeyTreatsTheSameFolderTypedTwoWaysAsOne(t *testing.T) {
	// Không phụ thuộc OS: config.json ghi trên Windows vẫn đọc được bằng bản
	// build Linux, nên khóa phải cho cùng kết quả ở cả hai nơi.
	same := []string{
		"D:\\work\\api",
		"d:\\work\\api",
		"D:/work/api",
		"D:\\work\\api\\",
		"  D:\\work\\api  ",
	}
	want := folderKey(same[0])
	for _, p := range same[1:] {
		if got := folderKey(p); got != want {
			t.Errorf("folderKey(%q) = %q, muốn %q", p, got, want)
		}
	}
	if folderKey("D:\\work\\api") == folderKey("D:\\work\\web") {
		t.Error("hai thư mục khác nhau không được trùng khóa")
	}
}

// ---------- rememberVersion không còn đẻ bản lưu ----------

func TestRememberVersionDoesNotCreateSnapshot(t *testing.T) {
	root := t.TempDir()
	s, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	before := len(s.Snapshots())
	envID := s.Config().Projects[0].Envs[0].ID

	for i := 0; i < 5; i++ {
		s.rememberVersion(envID, "9.9.9")
	}

	if got := len(s.Snapshots()); got != before {
		t.Errorf("có %d bản lưu, muốn %d — mỗi lần build đẻ một bản lưu sẽ chôn vùi màn Lịch sử cài đặt", got, before)
	}

	// Vẫn phải ghi xuống đĩa.
	reopened, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, e, ok := reopened.Config().FindEnv(envID); !ok || e.LastVersion != "9.9.9" {
		t.Errorf("lastVersion sau khi mở lại = %q", e.LastVersion)
	}
}

func TestSaveConfigStillCreatesSnapshot(t *testing.T) {
	root := t.TempDir()
	s, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	before := len(s.Snapshots())

	c := s.Config()
	c.DockerBin = "podman"
	if err := s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}

	if got := len(s.Snapshots()); got != before+1 {
		t.Fatalf("có %d bản lưu, muốn %d — sửa cài đặt vẫn phải khôi phục được", got, before+1)
	}
	raw, err := s.ReadSnapshot(s.Snapshots()[0].File)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := decodeConfig(raw); err != nil {
		t.Errorf("bản lưu do SaveConfig tạo ra không đọc được: %v", err)
	}
}

func TestRememberVersionDoesNotLoseSettingsSavedInParallel(t *testing.T) {
	root := t.TempDir()
	s, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	envID := s.Config().Projects[0].Envs[0].ID

	// Bỏ snapshot không được làm sống lại lỗi mất cập nhật: build ghi version
	// xen giữa lúc user lưu cài đặt thì cả hai thay đổi phải còn.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			s.rememberVersion(envID, "9.9.9")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			c := s.Config()
			c.DockerBin = "podman"
			_ = s.SaveConfig(c)
		}
	}()
	wg.Wait()

	reopened, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.Config()
	if got.DockerBin != "podman" {
		t.Errorf("dockerBin = %q, muốn podman — thay đổi user lưu bị ghi đè", got.DockerBin)
	}
	_, e, ok := got.FindEnv(envID)
	if !ok {
		t.Fatal("không tìm thấy env sau khi mở lại")
	}
	if e.LastVersion != "9.9.9" {
		t.Errorf("lastVersion = %q, muốn 9.9.9", e.LastVersion)
	}
}

// ---------- bản hỏng không phải bản khôi phục ----------

func TestSnapshotsExcludeBrokenConfigCopies(t *testing.T) {
	root := t.TempDir()
	s, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	writeVersionsFile(t, root, "config-20240101-000000.json", goodSnapshot)
	writeVersionsFile(t, root, "config-loi-20240102-000000.json", truncatedConfig)

	snaps := s.Snapshots()
	for _, snap := range snaps {
		if strings.HasPrefix(snap.File, "config-loi-") {
			t.Errorf("Snapshots trả về %q — bấm Khôi phục lên nó chỉ báo \"bản lưu hỏng\"", snap.File)
		}
	}
	if len(snaps) != 1 || snaps[0].File != "config-20240101-000000.json" {
		t.Errorf("Snapshots = %+v, muốn chỉ còn bản lưu tốt", snaps)
	}

	// File hỏng vẫn phải nằm trên đĩa: nó là bằng chứng để chẩn đoán.
	if findVersionsFile(t, root, "config-loi-") == "" {
		t.Error("file config-loi-* bị xóa mất")
	}
}

// ---------- v4 -> v5: thư mục mã nguồn chỉ còn ở cấp dự án ----------

// Đúng hình dạng đã gặp trong cấu hình thật: đường dẫn nằm ở môi trường đầu,
// dự án thì trống, nên môi trường thứ hai chẳng kế thừa được gì và không
// build được. Nâng lên cấp dự án là sửa luôn chỗ hỏng đó.
func TestMigrateV4LiftsTheSourceFolderUpToTheProject(t *testing.T) {
	c := normalise(migrateV4(Config{SchemaVersion: 4, Projects: []Project{{
		ID: "p", Name: "web", Image: "web",
		Envs: []Env{
			{ID: "a", Name: "Mặc định", Context: `D:\work\web`},
			{ID: "b", Name: "Môi trường 2"},
		},
	}}}))

	p := c.Projects[0]
	if p.Context != `D:\work\web` {
		t.Fatalf("thư mục của dự án = %q, muốn đúng thư mục đã nâng lên từ môi trường", p.Context)
	}
	for _, e := range p.Envs {
		if e.Context != "" {
			t.Errorf("môi trường %q còn giữ thư mục riêng %q", e.Name, e.Context)
		}
		if got := p.Effective(e).Context; got != `D:\work\web` {
			t.Errorf("môi trường %q build ở %q, muốn thư mục của dự án", e.Name, got)
		}
	}
}

// Dự án đã tự khai thư mục thì giữ nguyên: giá trị sót lại ở môi trường
// không được quyền lật ngược nó.
func TestMigrateV4KeepsTheProjectFolderWhenItHasOne(t *testing.T) {
	c := normalise(migrateV4(Config{SchemaVersion: 4, Projects: []Project{{
		ID: "p", Name: "web", Image: "web", Context: `D:\work\web`,
		Envs: []Env{{ID: "a", Name: "Staging", Context: `D:\somewhere\else`}},
	}}}))

	p := c.Projects[0]
	if p.Context != `D:\work\web` {
		t.Errorf("thư mục của dự án = %q, không được bị ghi đè", p.Context)
	}
	if got := p.Effective(p.Envs[0]).Context; got != `D:\work\web` {
		t.Errorf("hiệu lực = %q, muốn thư mục của dự án", got)
	}
}

// Thư mục mã nguồn không còn là override. Một giá trị sót lại trong file
// (sửa tay chẳng hạn) không được thay đổi thứ gì được build.
func TestEffectiveIgnoresAnEnvironmentSourceFolder(t *testing.T) {
	p := Project{Context: `D:\work\web`}
	if got := p.Effective(Env{Context: `D:\lung\tung`}).Context; got != `D:\work\web` {
		t.Errorf("= %q, muốn thư mục của dự án — môi trường không được ghi đè", got)
	}
}

func TestNormaliseClearsAnyEnvironmentSourceFolder(t *testing.T) {
	c := normalise(Config{Projects: []Project{{
		ID: "p", Name: "web", Image: "web", Context: `D:\work\web`,
		Envs: []Env{{ID: "a", Name: "Staging", Context: `D:\lung\tung`}},
	}}})
	if got := c.Projects[0].Envs[0].Context; got != "" {
		t.Errorf("= %q, muốn rỗng — dữ liệu không ai đọc thì không được nằm lại trên đĩa", got)
	}
}
