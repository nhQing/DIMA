package main

import (
	"strings"
	"testing"
	"time"
)

func TestRepoOfStripsTag(t *testing.T) {
	cases := map[string]string{
		"my-api:1.2.3":                      "my-api",
		"reg.local/team/my-api:prod-1.2.3":  "reg.local/team/my-api",
		"reg.local:5000/my-api:staging-1.0": "reg.local:5000/my-api",
		"reg.local:5000/my-api":             "reg.local:5000/my-api",
		"my-api@sha256:abc":                 "my-api",
		"reg.local/my-api:1.0@sha256:abc":   "reg.local/my-api",
	}
	for in, want := range cases {
		if got := repoOf(in); got != want {
			t.Errorf("repoOf(%q) = %q, muốn %q", in, got, want)
		}
	}
}

func TestPromoteStepsWithBuildxStayOnTheRegistry(t *testing.T) {
	steps := promoteSteps(true, "reg/app@sha256:abc", "reg/app:prod-1.0.0")

	if len(steps) != 1 {
		t.Fatalf("có %d bước, muốn 1 bước duy nhất", len(steps))
	}
	got := strings.Join(steps[0], " ")
	want := "buildx imagetools create -t reg/app:prod-1.0.0 reg/app@sha256:abc"
	if got != want {
		t.Errorf("lệnh = %q, muốn %q", got, want)
	}
	for _, s := range steps {
		if s[0] == "pull" || s[0] == "push" {
			t.Error("có buildx thì không được kéo/đẩy image qua máy này")
		}
	}
}

func TestPromoteStepsWithoutBuildxFallsBackToPullTagPush(t *testing.T) {
	steps := promoteSteps(false, "reg/app@sha256:abc", "reg/app:prod-1.0.0")

	if len(steps) != 3 {
		t.Fatalf("có %d bước, muốn 3", len(steps))
	}
	want := []string{
		"pull reg/app@sha256:abc",
		"tag reg/app@sha256:abc reg/app:prod-1.0.0",
		"push reg/app:prod-1.0.0",
	}
	for i, w := range want {
		if got := strings.Join(steps[i], " "); got != w {
			t.Errorf("bước %d = %q, muốn %q", i+1, got, w)
		}
	}
}

func TestBuildArgvUsesEffectiveEnvironment(t *testing.T) {
	p := Project{
		Context: "/src", Dockerfile: "Dockerfile", Platform: "linux/amd64",
		BuildArgs:  []KV{{Key: "APP", Value: "api"}},
		ExtraFlags: "--no-cache",
	}
	e := Env{
		Name: "Production", Target: "runtime",
		BuildArgs:  []KV{{Key: "NODE_ENV", Value: "production"}},
		Labels:     []KV{{Key: "env", Value: "prod"}},
		ExtraFlags: "--pull",
	}

	argv := buildArgv(p.Effective(e), "reg/app:prod-1.0.0")
	line := strings.Join(argv, " ")

	for _, want := range []string{
		"build -t reg/app:prod-1.0.0",
		"-f Dockerfile",
		"--build-arg APP=api",
		"--build-arg NODE_ENV=production",
		"--label env=prod",
		"--platform linux/amd64",
		"--target runtime",
		"--no-cache --pull",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("thiếu %q trong: %s", want, line)
		}
	}
	if argv[len(argv)-1] != "/src" {
		t.Errorf("tham số cuối = %q, muốn build context /src", argv[len(argv)-1])
	}
}

func TestBuildArgvDefaultsContextToCurrentDir(t *testing.T) {
	argv := buildArgv(Env{}, "app:1.0")
	if argv[len(argv)-1] != "." {
		t.Errorf("context mặc định = %q, muốn .", argv[len(argv)-1])
	}
}

func TestBuildArgvSkipsEmptyOptionalFlags(t *testing.T) {
	line := strings.Join(buildArgv(Env{Image: "app", Context: "."}, "app:1.0"), " ")
	for _, unwanted := range []string{"-f ", "--platform", "--target", "--build-arg", "--label"} {
		if strings.Contains(line, unwanted) {
			t.Errorf("không nên có %q khi bỏ trống: %s", unwanted, line)
		}
	}
}

// waitForBuild polls until a build leaves the running state.
func waitForBuild(t *testing.T, s *Store, id string) Build {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		b, ok := s.Build(id)
		if ok && b.Status != "running" {
			return b
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("build %s chưa xong sau 30 giây", id)
	return Build{}
}

func TestStartRunsACustomBuildCommand(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	env := Env{ID: newID(), Name: "Staging"}
	p := Project{
		ID: newID(), Name: "web", Image: "web", Context: t.TempDir(),
		BuildCommand: "echo phien-ban-{tag} cho {env}",
		Envs:         []Env{env},
	}

	started := s.Start(p, env, "1.2.3", false)

	if started.Command != "echo phien-ban-1.2.3 cho Staging" {
		t.Errorf("lệnh ghi lại = %q, muốn bản đã thay biến", started.Command)
	}
	// Test này kiểm việc thay biến và chạy lệnh, không kiểm kết quả build:
	// "echo" không gắn tag nào nên bản build kết thúc là hỏng, đúng như
	// TestStartFailsWhenTheCustomCommandProducesNoImage mô tả.
	done := waitForBuild(t, s, started.ID)
	if done.Status == "running" {
		t.Fatal("lệnh chưa chạy xong")
	}
	log, _ := s.LogSince(started.ID, 0)
	if !strings.Contains(log, "phien-ban-1.2.3 cho Staging") {
		t.Errorf("log không có output của lệnh:\n%s", log)
	}
}

// Lệnh chạy xong mà không để lại image mang đúng tên DIMA theo dõi thì bản
// build này không dùng được: không pull, không digest, không promote. Trước
// đây DIMA chỉ ghi một dòng nhắc rồi vẫn đi tiếp xuống lệnh push — nghĩa là
// nó đẩy bất cứ image cũ nào đang mang cái tag đó lên registry.
func TestStartFailsWhenTheCustomCommandProducesNoImage(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	env := Env{ID: newID(), Name: "Staging"}
	p := Project{
		ID: newID(), Name: "web", Image: "web", Context: t.TempDir(),
		BuildCommand: "echo khong tag gi ca",
		Envs:         []Env{env},
	}

	started := s.Start(p, env, "1.0.0", true) // có tick push
	done := waitForBuild(t, s, started.ID)

	if done.Status != "failed" {
		t.Errorf("status = %q, muốn failed: không có image thì bản này vô dụng", done.Status)
	}
	if done.Pushed {
		t.Error("đã đánh dấu pushed — không được đẩy khi chưa xác nhận có image")
	}
	log, _ := s.LogSince(started.ID, 0)
	if !strings.Contains(log, "không thấy image") {
		t.Errorf("phải nói rõ lệnh chưa gắn tag thành {ref}:\n%s", log)
	}
	if strings.Contains(log, "push web:1.0.0") {
		t.Errorf("đã chạy lệnh push dù không có image:\n%s", log)
	}
}

// Lệnh cũ còn dùng {version} phải hỏng thấy được ngay, chứ không được chạy
// với chuỗi "{version}" nguyên xi rồi gắn tag bậy.
func TestStartRefusesACommandStillUsingVersionVar(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	env := Env{ID: newID(), Name: "Staging", TagPrefix: "v"}
	p := Project{
		ID: newID(), Name: "web", Image: "web", Context: t.TempDir(),
		BuildCommand: "./build-and-push.bat {version}",
		Envs:         []Env{env},
	}

	started := s.Start(p, env, "1.2.2", false)
	done := waitForBuild(t, s, started.ID)

	if done.Status != "failed" {
		t.Fatalf("status = %q, muốn failed", done.Status)
	}
	if !strings.Contains(done.Err, "{tag}") {
		t.Errorf("lý do = %q, phải chỉ ra dùng {tag} thay thế", done.Err)
	}
}

func TestStartWithoutCustomCommandStillUsesDockerBuild(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	env := Env{ID: newID(), Name: "Staging"}
	p := Project{ID: newID(), Name: "web", Image: "web", Context: t.TempDir(), Envs: []Env{env}}

	started := s.Start(p, env, "1.0.0", false)
	waitForBuild(t, s, started.ID)

	if !strings.HasPrefix(started.Command, "docker build -t web:1.0.0") {
		t.Errorf("lệnh = %q, muốn vẫn là docker build khi không khai lệnh riêng", started.Command)
	}
}

// Lệnh build tự gắn tag thì nó không thể sai về việc có tạo ra image hay
// không — chỉ có thể lệch với cái tên DIMA đang chờ. Nói ra cái tên nó thật
// sự tạo ra là khác biệt giữa "build lại từ đầu" và "sửa một ô".
func TestNearMissesFindsTheSameImageUnderAnotherRegistry(t *testing.T) {
	imgs := []LocalImage{
		{Repository: "harbor.tech/mm/admin-mmt", Tag: "v1.2.2"},
		{Repository: "harbor.tech/mm/admin-mmt", Tag: "cache"},
		{Repository: "admin-mmt", Tag: "v1.0.0"},
	}
	got := nearMisses(imgs, "admin-mmt:v1.2.2")
	if len(got) != 1 || got[0] != "harbor.tech/mm/admin-mmt:v1.2.2" {
		t.Errorf("= %v, muốn đúng harbor.tech/mm/admin-mmt:v1.2.2", got)
	}
}

func TestNearMissesIgnoresOtherTagsAndOtherImages(t *testing.T) {
	imgs := []LocalImage{
		{Repository: "harbor.tech/mm/admin-mmt", Tag: "v1.2.1"},         // khác tag
		{Repository: "harbor.tech/mm/mai-money-website", Tag: "v1.2.2"}, // khác image
		{Repository: "admin-mmt", Tag: "v1.2.2"},                        // chính nó, không phải near miss
	}
	if got := nearMisses(imgs, "admin-mmt:v1.2.2"); len(got) != 0 {
		t.Errorf("= %v, muốn rỗng", got)
	}
}

func TestNearMissesHandlesARefWithARegistryPort(t *testing.T) {
	imgs := []LocalImage{{Repository: "localhost:5000/tool", Tag: "1.0.0"}}
	got := nearMisses(imgs, "tool:1.0.0")
	if len(got) != 1 || got[0] != "localhost:5000/tool:1.0.0" {
		t.Errorf("= %v, dấu hai chấm của cổng registry không phải dấu tách tag", got)
	}
}

// RepoDigests là danh sách theo từng repository, và một image có thể nằm
// trong nhiều repository. Lấy bừa phần tử đầu — như code cũ — có thể trả về
// digest của một repository hoàn toàn khác. Digest chính là thứ promote đem
// đi gắn tag, nên lấy nhầm là trỏ production vào sai bytes.
func TestDigestForRepoPicksTheMatchingRepository(t *testing.T) {
	list := []string{
		"docker.io/library/node@sha256:aaa",
		"harbor.tech/mm/admin-mmt@sha256:bbb",
	}
	if got := digestForRepo(list, "harbor.tech/mm/admin-mmt"); got != "sha256:bbb" {
		t.Errorf("= %q, muốn sha256:bbb", got)
	}
	if got := digestForRepo(list, "docker.io/library/node"); got != "sha256:aaa" {
		t.Errorf("= %q, muốn sha256:aaa", got)
	}
}

func TestDigestForRepoReturnsNothingWhenNoRepositoryMatches(t *testing.T) {
	// Image build tại chỗ rồi gắn tag: có thể mang digest của repository
	// khác, nhưng chưa từng lên registry dưới tên này.
	list := []string{"docker.io/library/node@sha256:aaa"}
	if got := digestForRepo(list, "harbor.tech/mm/admin-mmt"); got != "" {
		t.Errorf("= %q, muốn rỗng — chưa có digest cho chính repository này", got)
	}
}

func TestDigestForRepoOnAnEmptyList(t *testing.T) {
	// Image vừa build xong, chưa đi đâu cả.
	if got := digestForRepo(nil, "tool"); got != "" {
		t.Errorf("= %q, muốn rỗng", got)
	}
}
