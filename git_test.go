package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// newRepo builds a throwaway repository with one commit and returns its path.
func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("máy chạy test không có git")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "lần đầu")
	return dir
}

func TestReadGitOnACleanRepository(t *testing.T) {
	dir := newRepo(t)

	got := ReadGit(dir)

	if !got.IsRepo {
		t.Fatal("phải nhận ra đây là git repo")
	}
	if got.Branch != "main" {
		t.Errorf("nhánh = %q, muốn main", got.Branch)
	}
	if got.Commit == "" {
		t.Error("phải lấy được mã commit")
	}
	if len(got.Commit) > 12 {
		t.Errorf("commit = %q, muốn bản rút gọn", got.Commit)
	}
	if got.Dirty {
		t.Error("vừa commit xong thì cây làm việc phải sạch")
	}
}

func TestReadGitSeesUncommittedChanges(t *testing.T) {
	dir := newRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := ReadGit(dir); !got.Dirty {
		t.Error("sửa file mà chưa commit thì phải báo bẩn")
	}
}

func TestReadGitSeesUntrackedFiles(t *testing.T) {
	dir := newRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "moi.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := ReadGit(dir); !got.Dirty {
		t.Error("file mới chưa add cũng phải tính là bẩn")
	}
}

func TestReadGitFollowsBranchSwitch(t *testing.T) {
	dir := newRepo(t)
	cmd := exec.Command("git", "-C", dir, "checkout", "-b", "feature/thanh-toan")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("không tạo được nhánh: %v\n%s", err, out)
	}

	if got := ReadGit(dir).Branch; got != "feature/thanh-toan" {
		t.Errorf("nhánh = %q, muốn feature/thanh-toan", got)
	}
}

func TestReadGitOnAFolderThatIsNotARepository(t *testing.T) {
	got := ReadGit(t.TempDir())

	if got.IsRepo {
		t.Error("thư mục thường không được coi là repo")
	}
	if got.Branch != "" || got.Commit != "" {
		t.Errorf("không phải repo thì phải để trống, nhận %+v", got)
	}
}

func TestReadGitOnAnEmptyPath(t *testing.T) {
	if got := ReadGit("   "); got.IsRepo {
		t.Error("đường dẫn rỗng phải trả về không có gì")
	}
}
