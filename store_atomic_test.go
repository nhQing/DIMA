package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// goodSnapshot is a complete v2 config, the shape a file in versions/ has.
const goodSnapshot = `{
  "schemaVersion": 2,
  "dockerBin": "docker-tu-ban-luu",
  "projects": [
    {
      "id": "p1",
      "name": "Dự án đã lưu",
      "releaseMode": "rebuild",
      "image": "my-api",
      "envs": [{"id": "e1", "name": "Staging", "tagPrefix": "staging-"}]
    }
  ]
}`

// truncatedConfig is what a config.json cut off mid-write looks like.
const truncatedConfig = `{
  "schemaVersion": 2,
  "dockerBin": "docker",
  "projects": [
    {
      "id": "p1",
      "name": "Dự án đ`

func writeVersionsFile(t *testing.T, root, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "versions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "versions", name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findVersionsFile(t *testing.T, root, prefix string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "versions"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			return e.Name()
		}
	}
	return ""
}

func TestOpenStoreRecoversTruncatedConfigFromSnapshot(t *testing.T) {
	root := t.TempDir()
	writeVersionsFile(t, root, "config-20240101-000000.json", goodSnapshot)
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(truncatedConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := OpenStore(root)
	if err != nil {
		t.Fatalf("config cụt phải khôi phục được từ bản lưu, nhưng lỗi: %v", err)
	}

	c := s.Config()
	if c.DockerBin != "docker-tu-ban-luu" {
		t.Errorf("dockerBin = %q, muốn giá trị của bản lưu", c.DockerBin)
	}
	if len(c.Projects) != 1 || c.Projects[0].Name != "Dự án đã lưu" {
		t.Fatalf("config = %+v, muốn đúng nội dung bản lưu", c.Projects)
	}

	// File hỏng phải còn nguyên để soi lại, không bị ghi đè mất.
	broken := findVersionsFile(t, root, "config-loi-")
	if broken == "" {
		t.Fatal("không giữ lại file config.json hỏng trong versions/")
	}
	kept, err := os.ReadFile(filepath.Join(root, "versions", broken))
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != truncatedConfig {
		t.Error("bản giữ lại không khớp nội dung file hỏng ban đầu")
	}

	// config.json trên đĩa phải đọc được ngay, không cần khôi phục lại lần nữa.
	reopened, err := OpenStore(root)
	if err != nil {
		t.Fatalf("mở lại sau khôi phục vẫn lỗi: %v", err)
	}
	if reopened.Config().DockerBin != "docker-tu-ban-luu" {
		t.Error("config.json chưa được ghi lại từ bản lưu")
	}
}

func TestOpenStoreDoesNotSilentlyResetWhenNoSnapshotIsUsable(t *testing.T) {
	root := t.TempDir()
	// Bản lưu duy nhất cũng hỏng: không còn gì để khôi phục.
	writeVersionsFile(t, root, "config-20240101-000000.json", truncatedConfig)
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(truncatedConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenStore(root); err == nil {
		t.Fatal("hết bản lưu thì phải báo lỗi, không được rơi về config mặc định")
	}

	// File hỏng không được thay bằng config mặc định sau lưng người dùng.
	raw, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != truncatedConfig {
		t.Error("config.json bị ghi đè dù chưa khôi phục được")
	}
}

func TestOpenStoreSkipsUnreadableSnapshotsUntilOneDecodes(t *testing.T) {
	root := t.TempDir()
	// Bản mới nhất hỏng, bản cũ hơn còn tốt: phải lùi tiếp chứ không bỏ cuộc.
	writeVersionsFile(t, root, "config-20240101-000000.json", goodSnapshot)
	writeVersionsFile(t, root, "config-20240102-000000.json", "{ hỏng")
	older := filepath.Join(root, "versions", "config-20240101-000000.json")
	newer := filepath.Join(root, "versions", "config-20240102-000000.json")
	info, err := os.Stat(newer)
	if err != nil {
		t.Fatal(err)
	}
	// Snapshots() xếp theo mtime, nên phải dựng lại thứ tự thật.
	if err := os.Chtimes(older, info.ModTime().Add(-time.Hour), info.ModTime().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(truncatedConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := OpenStore(root)
	if err != nil {
		t.Fatalf("phải khôi phục từ bản lưu cũ hơn, nhưng lỗi: %v", err)
	}
	if s.Config().DockerBin != "docker-tu-ban-luu" {
		t.Errorf("dockerBin = %q, muốn bản lưu tốt duy nhất", s.Config().DockerBin)
	}
}

// Bản lưu cho rememberVersion đã bị bỏ có chủ ý: giữ khoá xuyên suốt đã
// chặn xong việc mất cập nhật, nên dưới khoá không còn gì để khôi phục, còn
// một bản lưu mỗi lần build thì làm ngập màn "Lịch sử cài đặt".
// TestRememberVersionDoesNotCreateSnapshot và TestSaveConfigStillCreatesSnapshot
// trong store_schema3_test.go giữ phần kiểm chứng đó.

func TestRememberVersionKeepsSettingsSavedMeanwhile(t *testing.T) {
	root := t.TempDir()
	s, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	envID := s.Config().Projects[0].Envs[0].ID

	// User lưu cài đặt, rồi build xong mới ghi version: bản ghi sau không
	// được mang theo ảnh chụp cũ và xoá mất thay đổi vừa lưu.
	c := s.Config()
	c.DockerBin = "podman"
	if err := s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	s.rememberVersion(envID, "9.9.9")

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

func TestStoreNeverWritesConfigOrHistoryInPlace(t *testing.T) {
	raw, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatal(err)
	}
	// os.WriteFile ghi thẳng vào file đích: đứt giữa chừng là file cụt.
	// Mọi đường ghi của store phải đi qua writeFileAtomic.
	if strings.Contains(string(raw), "os.WriteFile(") {
		t.Error("store.go còn os.WriteFile — phải dùng writeFileAtomic để ghi nguyên tử")
	}
	if !strings.Contains(string(raw), "os.Rename(") {
		t.Error("store.go không có os.Rename — ghi không thể nguyên tử")
	}
}

func TestWriteFileAtomicReplacesAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("cũ"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := writeFileAtomic(path, []byte("mới"), 0o644); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "mới" {
		t.Errorf("nội dung = %q, muốn mới", raw)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("file .tmp còn sót lại sau khi ghi xong")
	}
}
