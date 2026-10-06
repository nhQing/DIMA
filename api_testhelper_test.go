package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestAPI wires the HTTP surface the same way main.go does, over a store
// rooted in a temporary folder. Going through the mux rather than calling the
// handler methods directly is deliberate: the route pattern and the status
// code are part of what these tests are checking.
func newTestAPI(t *testing.T) (*Store, *httptest.Server) {
	t.Helper()
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("không mở được store trong thư mục tạm: %v", err)
	}
	mux := http.NewServeMux()
	(&api{store: store}).routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return store, srv
}
