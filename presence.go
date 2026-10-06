package main

import (
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// presence counts the UI windows currently connected to this server.
//
// Closing the window has to free the port — that is the whole reason DIMA is
// a windowed app rather than a terminal one. It used to decide "the window is
// closed" by watching the browser process it launched, and that proxy is
// wrong in three ways that all end identically:
//
//   - A Chromium that already has this profile open hands the request to the
//     running process and exits immediately. watchWindow reads a return
//     inside three seconds as "that was not our window" and stops watching,
//     permanently. The window later closes with nobody looking.
//   - While a build is running, watchWindow prints a note and returns. The
//     build ends minutes later and nothing ever looks again.
//   - With -tab there is no process to watch at all.
//
// Each case leaves a server with no window, holding the port until someone
// finds it in Task Manager — and build.ps1 then refuses to overwrite the
// locked .exe, which is how this surfaced.
//
// Counting open connections answers the real question instead of a stand-in
// for it. The page holds one event-stream request open and the operating
// system tears it down the moment the window goes, no matter which process
// owned it.
type presence struct {
	mu    sync.Mutex
	open  int
	seen  bool      // at least one window has ever connected
	empty time.Time // when the last one disconnected
}

func (p *presence) enter() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.open++
	p.seen = true
}

func (p *presence) leave() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.open > 0 {
		p.open--
	}
	if p.open == 0 {
		p.empty = time.Now()
	}
}

// idle reports whether every window has been gone for at least d.
//
// It stays false until a window has connected once, so a server started with
// -no-open, or one whose browser has not finished loading the page yet, never
// mistakes "nobody has arrived" for "everybody left".
func (p *presence) idle(d time.Duration) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.seen || p.open > 0 {
		return false
	}
	return time.Since(p.empty) >= d
}

// stream is the connection the page keeps open for as long as its window
// lives. It sends nothing the UI reads; the connection itself is the signal.
func (p *presence) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "server không hỗ trợ luồng", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": mo\n\n")
	flusher.Flush()

	p.enter()
	defer p.leave()

	// A proxy or a sleeping machine can leave a dead socket looking healthy.
	// Writing a comment periodically is what eventually fails and releases
	// this handler in that case.
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			if _, err := fmt.Fprint(w, ": con day\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// supervise quits the process once every window has been gone for grace.
//
// The wait matters: reloading the page closes one stream and opens the next a
// moment later, and quitting in that gap would kill the app under the user's
// hands.
func (p *presence) supervise(store *Store, grace time.Duration) {
	for {
		time.Sleep(time.Second)
		if !p.idle(grace) {
			continue
		}
		// A running build deliberately outlives its window: the person
		// started that work and losing it to a closed window would be worse
		// than a port held a few minutes longer. Unlike the code this
		// replaces, the check runs again every second, so DIMA does quit
		// once the build finishes.
		if store != nil && store.HasRunningBuild() {
			continue
		}
		fmt.Println("\nKhông còn cửa sổ nào mở. Dừng ứng dụng.")
		os.Exit(0)
	}
}
