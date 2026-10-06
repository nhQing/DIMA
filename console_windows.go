//go:build windows

package main

import (
	"log"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"
)

// The Windows binary is linked with -H windowsgui, so opening it from a
// shortcut does not pop up a console window. That flag also cuts the program
// off from a terminal it was started in, which would leave a command line run
// printing nothing at all. The two helpers here put the output back where a
// person can actually see it.
//
// Both use syscall against the system DLLs, so this stays pure Go with no
// CGO and the binary stays a single static file.

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	user32   = syscall.NewLazyDLL("user32.dll")
)

// attachConsole reconnects stdout and stderr to the console the program was
// started from, when there is one. Started from Explorer or a shortcut there
// is none, and this does nothing.
func attachConsole() {
	const attachParentProcess = ^uintptr(0) // (DWORD)-1
	if r, _, _ := kernel32.NewProc("AttachConsole").Call(attachParentProcess); r == 0 {
		return
	}
	f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0)
	if err != nil {
		return
	}
	os.Stdout = f
	os.Stderr = f
	log.SetOutput(f)
}

// hideWindow stops a child process from opening a console window.
//
// docker.exe is a console program. When a windowed process launches one,
// Windows gives it a console of its own, which flashes on screen and then
// vanishes. DIMA asks docker for its version every few seconds, so without
// this the screen flickers the whole time the app is open.
func hideWindow(cmd *exec.Cmd) {
	const createNoWindow = 0x08000000
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}

// shellCommand wraps a custom build command so the platform's shell runs it.
//
// PowerShell rather than cmd: cmd cannot run a .ps1 at all and does not
// understand ./script, while PowerShell handles .ps1, .bat and npm run
// alike. Running through a shell is also what makes pipes, && and quoting
// behave the way the person expects from their terminal.
func shellCommand(command string) (string, []string) {
	return "powershell.exe", []string{"-NoProfile", "-Command", command}
}

// killTree ends a child process and everything that child started.
//
// Go's context cancellation calls Process.Kill, which here is TerminateProcess
// against that one process. But the build almost never runs in that process:
// `docker build` hands the work to the Docker daemon, and a custom build
// command runs through powershell, whose own children outlive it. Killing only
// the direct child stops the messenger and leaves the work running — which is
// exactly why pressing "Dừng build" looked like it did nothing.
//
// taskkill /T walks the whole tree. It cannot reach work that has already left
// this machine's process tree — a build running inside the Docker daemon keeps
// going — so the caller still has to say so rather than claim the build stopped.
func killTree(pid int) error {
	cmd := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	hideWindow(cmd)
	return cmd.Run()
}

// groupChild is a no-op on Windows: taskkill /T finds the descendants through
// the parent ids, so the child needs no special grouping when it starts.
func groupChild(cmd *exec.Cmd) {}

// alert shows a message box. Without a console this is the only way to tell
// the user why the application refused to start.
func alert(title, body string) {
	const mbIconError = 0x10
	t, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	b, err := syscall.UTF16PtrFromString(body)
	if err != nil {
		return
	}
	user32.NewProc("MessageBoxW").Call(0,
		uintptr(unsafe.Pointer(b)), uintptr(unsafe.Pointer(t)), mbIconError)
}
