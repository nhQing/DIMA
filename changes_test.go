package main

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// commitIn adds one commit to a repository made by newRepo.
func commitIn(t *testing.T, dir, file, msg string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(msg), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", msg}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return ReadGit(dir).Commit
}

func TestCommitsBetweenListsOnlyWhatIsNew(t *testing.T) {
	dir := newRepo(t)
	from := ReadGit(dir).Commit
	commitIn(t, dir, "a.txt", "sửa lỗi đăng nhập")
	to := commitIn(t, dir, "b.txt", "thêm trang báo cáo")

	got, err := CommitsBetween(dir, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Subject != "thêm trang báo cáo" || got[1].Subject != "sửa lỗi đăng nhập" {
		t.Errorf("= %+v", got)
	}
	if _, err := CommitsBetween(dir, from, "deadbee"); err == nil {
		t.Error("commit không có trong thư mục thì phải báo lỗi, không được coi là không có thay đổi")
	}
}

func TestBuildChangesComparesWithThePreviousBuildOfTheSameEnv(t *testing.T) {
	dir := newRepo(t)
	c1 := ReadGit(dir).Commit
	commitIn(t, dir, "a.txt", "sửa lỗi đăng nhập")
	c2 := commitIn(t, dir, "b.txt", "thêm trang báo cáo")

	store, srv := newTestAPI(t)
	p := Project{ID: "p", Name: "web", Image: "web", Context: dir,
		Envs: []Env{{ID: "stg", Name: "Staging"}, {ID: "prd", Name: "Production"}}}
	if err := store.SaveConfig(Config{Projects: []Project{p}}); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().Add(-time.Hour)
	store.addBuild(Build{ID: "old", ProjectID: "p", EnvID: "stg", Tag: "1.0.0", Commit: c1, Status: "success", StartedAt: t0})
	// Một bản của môi trường khác ở giữa không được tính là "bản trước".
	store.addBuild(Build{ID: "other", ProjectID: "p", EnvID: "prd", Commit: c2, Status: "success", StartedAt: t0.Add(time.Minute)})
	store.addBuild(Build{ID: "new", ProjectID: "p", EnvID: "stg", Tag: "1.1.0", Commit: c2, Status: "success", StartedAt: t0.Add(2 * time.Minute)})

	resp, err := http.Get(srv.URL + "/api/build/new/changes")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		From, FromVersion string
		Commits           []Commit
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusOK || body.From != c1 || body.FromVersion != "1.0.0" || len(body.Commits) != 2 {
		t.Errorf("status=%d body=%+v", resp.StatusCode, body)
	}

	// Lùi về commit cũ: không được trả "không có thay đổi", mà phải nói bản
	// này bỏ đi những commit nào.
	store.addBuild(Build{ID: "back", ProjectID: "p", EnvID: "stg", Tag: "1.0.1", Commit: c1, Status: "success", StartedAt: t0.Add(3 * time.Minute)})
	resp2, err := http.Get(srv.URL + "/api/build/back/changes")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var back struct{ Commits, Removed []Commit }
	json.NewDecoder(resp2.Body).Decode(&back)
	if len(back.Commits) != 0 || len(back.Removed) != 2 {
		t.Errorf("rollback: commits=%d removed=%d, muốn 0 và 2", len(back.Commits), len(back.Removed))
	}
}
