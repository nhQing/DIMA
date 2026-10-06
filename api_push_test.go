package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// postPush calls the push endpoint and returns the status code and the error
// message the server gave.
func postPush(t *testing.T, srv string, id string) (int, string) {
	t.Helper()
	resp, err := http.Post(srv+"/api/build/"+id+"/push", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("gọi push lỗi: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body.Error
}

// TestPushIsRefusedForBuildsWhoseTagIsNotTheirOwn covers the case that made
// this gate necessary: docker only moves a tag when the build succeeds, so a
// canceled or failed build leaves the previous image sitting under its ref.
// A running build has not produced anything yet.
func TestPushIsRefusedForBuildsWhoseTagIsNotTheirOwn(t *testing.T) {
	for _, status := range []string{"canceled", "failed", "running"} {
		t.Run(status, func(t *testing.T) {
			store, srv := newTestAPI(t)
			b := Build{
				ID: newID(), ProjectID: newID(), ProjectName: "web",
				EnvID: newID(), EnvName: "Production",
				Version: "1.2.3", Tag: "prod-1.2.3", Ref: "harbor.tech/mm/web:prod-1.2.3",
				Kind: KindBuild, Status: status, StartedAt: time.Now(),
			}
			store.addBuild(b)

			code, msg := postPush(t, srv.URL, b.ID)
			if code != http.StatusConflict {
				t.Fatalf("status = %d, muốn 409 cho bản %s", code, status)
			}
			if strings.TrimSpace(msg) == "" {
				t.Error("server từ chối nhưng không nói lý do")
			}

			// The record has to come out of a refused push untouched: a status
			// flipped to success, a digest or a pushed flag would each be
			// enough to let promote ship these bytes to another environment.
			after, ok := store.Build(b.ID)
			if !ok {
				t.Fatal("bản build biến mất khỏi lịch sử")
			}
			if after.Status != status {
				t.Errorf("status = %q sau khi bị từ chối, muốn giữ nguyên %q", after.Status, status)
			}
			if after.Pushed {
				t.Error("bản bị từ chối vẫn bị đánh dấu đã push")
			}
			if after.Digest != "" {
				t.Errorf("digest = %q, bản bị từ chối không được có digest", after.Digest)
			}
		})
	}
}

// A build with no ref names no image, so there is nothing to push even when
// it is marked success — an imported row that never got a ref, for instance.
func TestPushIsRefusedWhenTheBuildHasNoRef(t *testing.T) {
	store, srv := newTestAPI(t)
	b := Build{ID: newID(), ProjectName: "web", EnvName: "Production",
		Version: "1.0.0", Kind: KindImport, Status: "success", StartedAt: time.Now()}
	store.addBuild(b)

	code, msg := postPush(t, srv.URL, b.ID)
	if code != http.StatusConflict {
		t.Fatalf("status = %d, muốn 409 khi bản build không có ref", code)
	}
	if strings.TrimSpace(msg) == "" {
		t.Error("server từ chối nhưng không nói lý do")
	}
	if after, _ := store.Build(b.ID); after.Pushed {
		t.Error("bản không có ref vẫn bị đánh dấu đã push")
	}
}

func TestPushOnAnUnknownBuildIsNotFound(t *testing.T) {
	_, srv := newTestAPI(t)
	if code, _ := postPush(t, srv.URL, "khong-co-that"); code != http.StatusNotFound {
		t.Fatalf("status = %d, muốn 404", code)
	}
}

// The UI reads canPush and pushBlocked instead of deriving the rule again
// from status, so the state endpoint has to carry both.
func TestStateSaysWhetherEachBuildCanBePushed(t *testing.T) {
	store, srv := newTestAPI(t)
	blocked := Build{ID: newID(), ProjectName: "web", EnvName: "Production",
		Version: "1.2.3", Ref: "harbor.tech/mm/web:prod-1.2.3",
		Kind: KindBuild, Status: "canceled", StartedAt: time.Now()}
	ok := Build{ID: newID(), ProjectName: "web", EnvName: "Staging",
		Version: "1.2.3", Ref: "harbor.tech/mm/web:staging-1.2.3",
		Kind: KindBuild, Status: "success", StartedAt: time.Now().Add(-time.Minute)}
	store.addBuild(blocked)
	store.addBuild(ok)

	resp, err := http.Get(srv.URL + "/api/state")
	if err != nil {
		t.Fatalf("gọi state lỗi: %v", err)
	}
	defer resp.Body.Close()
	var state struct {
		Builds []struct {
			ID          string `json:"id"`
			Status      string `json:"status"`
			CanPush     bool   `json:"canPush"`
			PushBlocked string `json:"pushBlocked"`
		} `json:"builds"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatalf("state trả JSON không đọc được: %v", err)
	}

	seen := 0
	for _, b := range state.Builds {
		switch b.ID {
		case blocked.ID:
			seen++
			if b.CanPush {
				t.Error("bản canceled vẫn báo canPush = true")
			}
			if strings.TrimSpace(b.PushBlocked) == "" {
				t.Error("bản canceled không kèm lý do trong pushBlocked")
			}
		case ok.ID:
			seen++
			if !b.CanPush {
				t.Errorf("bản success báo canPush = false (%s)", b.PushBlocked)
			}
			if b.PushBlocked != "" {
				t.Errorf("pushBlocked = %q, bản push được thì phải rỗng", b.PushBlocked)
			}
		}
		// The embedded build still has to serialise as it always did, or the
		// rest of the screen loses its data.
		if b.Status == "" {
			t.Error("state thiếu trường status của build")
		}
	}
	if seen != 2 {
		t.Fatalf("state chỉ trả %d trong 2 bản build đã thêm", seen)
	}
}

// Reopening a log must append. Pushing a build from an earlier session goes
// through logFor again, and truncating there would erase the build output
// that is the only record of how the image was made.
func TestLogForKeepsWhatIsAlreadyOnDisk(t *testing.T) {
	root := t.TempDir()
	s, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	id := newID()
	s.logFor(id).writeString("dòng build đầu tiên\n")
	if s.logFor(id) != s.logFor(id) {
		t.Fatal("logFor trả hai buffer khác nhau cho cùng một build")
	}
	if text, _ := s.LogSince(id, 0); !strings.Contains(text, "dòng build đầu tiên") {
		t.Fatalf("mất log ngay trong cùng một phiên:\n%s", text)
	}
	s.logs[id].close()

	// A second session: the buffer is gone from memory, so logFor has to
	// reopen the file — the exact path a later push takes.
	s2, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	s2.logFor(id).writeString("dòng push thêm sau\n")
	// Windows không xoá được file đang mở, nên t.TempDir() dọn dẹp sẽ hỏng
	// nếu phiên thứ hai còn giữ handle.
	defer s2.logs[id].close()

	raw, err := os.ReadFile(s2.logPath(id))
	if err != nil {
		t.Fatalf("không đọc được file log: %v", err)
	}
	for _, want := range []string{"dòng build đầu tiên", "dòng push thêm sau"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("file log thiếu %q:\n%s", want, raw)
		}
	}

	// The in-memory buffer has to be seeded from the file, otherwise LogSince
	// hands the UI offsets that do not line up with what is on disk.
	text, next := s2.LogSince(id, 0)
	if next != len(raw) {
		t.Errorf("offset = %d, file dài %d — buffer lệch so với file", next, len(raw))
	}
	if !strings.Contains(text, "dòng build đầu tiên") {
		t.Errorf("LogSince không nạp lại phần log cũ:\n%s", text)
	}
}

// Đã push rồi thì không được push lại: tag trên máy có thể đã bị một lần
// build sau kéo sang image khác, nên push lại bản cũ sẽ đẩy nhầm bytes rồi
// gắn digest của chúng vào bản ghi cũ.
func TestPushRejectedForAlreadyPushedBuild(t *testing.T) {
	b := Build{ID: "x", Ref: "reg/app:1.0.0", Status: "success", Pushed: true}

	err := canPush(b)

	if err == nil {
		t.Fatal("bản đã push phải bị từ chối")
	}
	if !strings.Contains(err.Error(), "đã đẩy lên registry") {
		t.Errorf("lý do = %q, muốn nói rõ là đã push rồi", err.Error())
	}
}

func TestPushAllowedForSuccessfulBuildNotYetPushed(t *testing.T) {
	b := Build{ID: "x", Ref: "reg/app:1.0.0", Status: "success"}

	if err := canPush(b); err != nil {
		t.Errorf("bản thành công chưa push phải push được, nhận: %v", err)
	}
}

// Bản "đọc vào" từ docker images chưa push bao giờ, và đó chính là đường
// duy nhất để nó có digest — phải push được.
func TestPushAllowedForImportedBuild(t *testing.T) {
	b := Build{ID: "x", Ref: "reg/app:cache", Status: "success", Kind: KindImport}

	if err := canPush(b); err != nil {
		t.Errorf("bản đọc vào phải push được, nhận: %v", err)
	}
}
