package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// guard phải chặn trang web khác ghi vào DIMA, kể cả khi Host là loopback —
// fetch(..., {mode:'no-cors'}) từ một trang bất kỳ gửi được text/plain mà
// không cần preflight, và DIMA chạy lệnh.
func TestGuardBlocksCrossSiteWrites(t *testing.T) {
	reached := false
	h := guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	cases := []struct {
		name    string
		method  string
		headers map[string]string
		ok      bool
	}{
		{"giao diện của DIMA", "POST", map[string]string{"Content-Type": "application/json", "Origin": "http://127.0.0.1:7788", "Sec-Fetch-Site": "same-origin"}, true},
		{"không có Origin (curl, test)", "POST", map[string]string{"Content-Type": "application/json"}, true},
		{"đọc thì luôn được", "GET", nil, true},
		{"no-cors text/plain", "POST", map[string]string{"Content-Type": "text/plain;charset=UTF-8", "Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}, false},
		{"form", "POST", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, false},
		{"JSON từ trang khác", "PUT", map[string]string{"Content-Type": "application/json", "Origin": "https://evil.example"}, false},
		{"JSON từ site khác", "POST", map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"}, false},
	}
	for _, tc := range cases {
		reached = false
		req := httptest.NewRequest(tc.method, "http://127.0.0.1:7788/api/build", strings.NewReader("{}"))
		for k, v := range tc.headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if reached != tc.ok {
			t.Errorf("%s: tới được handler = %v, muốn %v (status %d)", tc.name, reached, tc.ok, rec.Code)
		}
	}
}
