package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

//go:embed ui
var uiFiles embed.FS

const appVersion = "0.2.0"

// buildStamp identifies one particular build, set at link time by build.ps1
// and build.sh.
//
// It also gives every build a different hash on purpose. Go builds are
// reproducible, so the same source always produces the same bytes — and
// Windows Smart App Control caches its verdict per file hash. Without a
// stamp, a build whose hash happened to be refused stays refused no matter
// how many times it is rebuilt. This does not make the binary trusted; SAC
// still judges each one. Signing is the only thing that settles it for good.
var buildStamp = "dev"

func main() {
	// Windows builds have no console of their own; pick up the terminal's if
	// this was started from one.
	attachConsole()

	var (
		port    = flag.Int("port", 7788, "cổng lắng nghe trên localhost")
		dataDir = flag.String("data", "", "thư mục dữ liệu (mặc định ~/.dima)")
		noOpen  = flag.Bool("no-open", false, "không tự mở giao diện")
		asTab   = flag.Bool("tab", false, "mở trong tab trình duyệt thường thay vì cửa sổ riêng")
	)
	flag.Parse()

	root := *dataDir
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fatal("Không xác định được thư mục home", err.Error())
		}
		root = filepath.Join(home, ".dima")
	}

	profile := filepath.Join(root, "window")

	// Only honour the port as an exact requirement when the user asked for
	// one; otherwise a busy port is something to route around, not to report.
	fixedPort := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "port" {
			fixedPort = true
		}
	})

	ln, chosen, running := bind(*port, fixedPort)
	if running != "" {
		// A copy of DIMA already owns the port. Showing its window is what
		// the person wanted anyway, so do that instead of complaining.
		fmt.Printf("DIMA đã chạy sẵn tại %s — mở cửa sổ của bản đó.\n", running)
		if !*noOpen {
			show(running, profile, *asTab, nil)
		}
		return
	}
	if ln == nil {
		fatal("Không mở được cổng", fmt.Sprintf(
			"Cổng %d đang bị một chương trình khác chiếm.\n\nBỏ cờ -port để DIMA tự chọn cổng trống.", *port))
	}

	addr := fmt.Sprintf("127.0.0.1:%d", chosen)
	url := "http://" + addr

	store, err := OpenStore(root)
	if err != nil {
		fatal("Không mở được dữ liệu", err.Error())
	}

	sub, err := fs.Sub(uiFiles, "ui")
	if err != nil {
		fatal("Không đọc được giao diện nhúng", err.Error())
	}

	mux := http.NewServeMux()
	(&api{store: store}).routes(mux)

	// The window's own lifeline: while a page is open it holds this request,
	// and when the last one drops the supervisor shuts the app down so the
	// port is free again. See presence.go for why watching the browser
	// process was not enough.
	pres := &presence{}
	mux.HandleFunc("GET /api/alive", pres.stream)
	go pres.supervise(store, 10*time.Second)

	mux.Handle("/", http.FileServer(http.FS(sub)))

	fmt.Printf("DIMA %s (bản build %s)\n", appVersion, buildStamp)
	fmt.Printf("Dữ liệu: %s\n", root)
	fmt.Printf("Đang chạy tại %s  (Ctrl+C để dừng)\n", url)

	if !*noOpen {
		go func() {
			time.Sleep(300 * time.Millisecond)
			show(url, profile, *asTab, store)
		}()
	}

	srv := &http.Server{Handler: guard(mux)}
	if err := srv.Serve(ln); err != nil {
		log.Fatal(err)
	}
}

// fatal reports a startup failure and stops. Without a console on Windows a
// printed message would vanish, so it also puts up a dialog.
func fatal(title, body string) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", title, body)
	alert("DIMA — "+title, body)
	os.Exit(1)
}

// bind opens a listener on the loopback interface only: this app can run
// docker commands, so it must never be reachable from the network.
//
// A busy port is not worth bothering the user about, so unless they named a
// port it walks forward until it finds a free one. It returns the URL of an
// existing DIMA instead if one already holds a port along the way, and a nil
// listener when nothing worked.
func bind(start int, fixed bool) (net.Listener, int, string) {
	tries := 20
	if fixed {
		tries = 1
	}
	for p := start; p < start+tries; p++ {
		addr := fmt.Sprintf("127.0.0.1:%d", p)
		if ln, err := net.Listen("tcp", addr); err == nil {
			return ln, p, ""
		}
		if url := "http://" + addr; alreadyServing(url) {
			return nil, p, url
		}
	}
	return nil, 0, ""
}

// alreadyServing reports whether the address is held by another DIMA rather
// than by some unrelated program.
func alreadyServing(url string) bool {
	c := http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := c.Get(url + "/api/state")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var probe struct {
		AppVersion string `json:"appVersion"`
	}
	return json.NewDecoder(resp.Body).Decode(&probe) == nil && probe.AppVersion != ""
}

// show opens the interface, as its own window when it can. A store means we
// own this server, so closing the window should stop it.
func show(url, profile string, asTab bool, store *Store) {
	if !asTab {
		if cmd := openAppWindow(url, profile); cmd != nil {
			if store != nil {
				go watchWindow(cmd, store)
			}
			return
		}
	}
	openBrowser(url)
}

// guard blocks requests whose Host header is not loopback, which stops a
// malicious page from reaching this server through DNS rebinding.
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			http.Error(w, "chỉ truy cập được qua localhost", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// openAppWindow opens the UI as its own window using a Chromium browser's
// app mode: no address bar, no tab strip, its own taskbar entry and icon.
// That is as close to a native window as this app can get while staying a
// single static binary with no CGO and no runtime to install.
//
// It returns the browser process, or nil when no such browser was found.
func openAppWindow(url, profileDir string) *exec.Cmd {
	// A private profile directory keeps this window out of the user's normal
	// browsing session. It also means a second launch of DIMA reuses this
	// same profile, so the browser raises the existing window instead of
	// opening a second one.
	args := []string{
		"--app=" + url,
		"--user-data-dir=" + profileDir,
		"--window-size=1280,860",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
	}
	for _, bin := range chromiumCandidates() {
		cmd := exec.Command(bin, args...)
		hideWindow(cmd)
		if err := cmd.Start(); err != nil {
			continue
		}
		return cmd
	}
	return nil
}

// watchWindow shuts the app down when its window is closed, the way a
// desktop application behaves — which also frees the port.
func watchWindow(cmd *exec.Cmd, store *Store) {
	started := time.Now()
	_ = cmd.Wait()

	// Some browsers hand the request to another process and exit at once.
	// Treat an immediate return as "that was not our window" and keep the
	// server up rather than quitting in the user's face.
	if time.Since(started) < 3*time.Second {
		return
	}
	if store.HasRunningBuild() {
		fmt.Println("\nCửa sổ đã đóng nhưng còn build đang chạy, nên ứng dụng vẫn tiếp tục.")
		fmt.Println("Ctrl+C để dừng hẳn.")
		return
	}
	fmt.Println("\nCửa sổ đã đóng. Dừng ứng dụng.")
	os.Exit(0)
}

// chromiumCandidates lists the browsers that support app mode, best first.
func chromiumCandidates() []string {
	switch runtime.GOOS {
	case "windows":
		var out []string
		for _, base := range []string{
			os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData"),
		} {
			if base == "" {
				continue
			}
			out = append(out,
				filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"),
				filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"),
				filepath.Join(base, "BraveSoftware", "Brave-Browser", "Application", "brave.exe"),
			)
		}
		return append(out, "msedge.exe", "chrome.exe")
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		}
	default:
		return []string{"google-chrome", "chromium", "chromium-browser", "microsoft-edge", "brave-browser"}
	}
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	hideWindow(cmd)
	_ = cmd.Start()
}
