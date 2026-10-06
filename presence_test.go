package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPresenceStaysQuietBeforeAnyWindowConnects(t *testing.T) {
	// Chạy với -no-open thì không có cửa sổ nào cả. "Chưa ai đến" không được
	// hiểu thành "mọi người đã rời đi", nếu không app tự tắt ngay khi khởi
	// động.
	var p presence
	if p.idle(0) {
		t.Error("coi là rảnh khi chưa có cửa sổ nào từng nối — sẽ tự tắt ngay lúc mở")
	}
}

func TestPresenceIsNotIdleWhileAWindowIsOpen(t *testing.T) {
	var p presence
	p.enter()
	if p.idle(0) {
		t.Error("còn cửa sổ mở mà đã coi là rảnh")
	}
}

func TestPresenceBecomesIdleOnlyAfterTheGracePeriod(t *testing.T) {
	var p presence
	p.enter()
	p.leave()

	// Tải lại trang là đóng một kết nối rồi mở kết nối mới ngay sau đó. Tắt
	// app trong khoảng hở ấy là giết app ngay dưới tay người dùng.
	if p.idle(time.Hour) {
		t.Error("tắt ngay khi cửa sổ vừa rời — chưa chờ hết thời gian ân hạn")
	}
	if !p.idle(0) {
		t.Error("hết thời gian ân hạn mà vẫn không coi là rảnh")
	}
}

func TestPresenceNeedsEveryWindowGone(t *testing.T) {
	var p presence
	p.enter()
	p.enter() // hai cửa sổ
	p.leave()
	if p.idle(0) {
		t.Error("mới đóng một cửa sổ đã coi là hết — cửa sổ còn lại sẽ bị tắt theo")
	}
	p.leave()
	if !p.idle(0) {
		t.Error("đóng hết rồi mà vẫn chưa coi là rảnh")
	}
}

func TestPresenceStreamCountsAndReleases(t *testing.T) {
	var p presence
	srv := httptest.NewServer(http.HandlerFunc(p.stream))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	if _, err := resp.Body.Read(buf); err != nil { // chờ tới khi handler đã chạy
		t.Fatal(err)
	}
	if p.idle(0) {
		t.Error("đang có kết nối mà đã coi là rảnh")
	}

	resp.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p.idle(0) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("đóng kết nối rồi mà server vẫn tưởng cửa sổ còn mở — cổng sẽ bị giữ mãi")
}
