//go:build !windows

package main

// On macOS and Linux a program started from a terminal keeps its output, and
// one started from a launcher simply has none. Neither needs the Windows
// console juggling, so these are no-ops.

import (
	"os/exec"
	"syscall"
)

func attachConsole() {}

func alert(title, body string) {}

func hideWindow(cmd *exec.Cmd) {}

// groupChild puts the child at the head of its own process group, so the whole
// group can be signalled at once later. Without it, killing the child leaves
// whatever that child started still running — see killTree.
func groupChild(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killTree ends a child process and everything it started. A negative pid
// addresses the process group that groupChild created.
//
// It cannot reach work that has left this machine's process tree: a build
// running inside the Docker daemon keeps going, so the caller still has to say
// so rather than claim the build stopped.
func killTree(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}

// shellCommand wraps a custom build command so the platform's shell runs it,
// which is what makes pipes, && and quoting behave as they do in a terminal.
func shellCommand(command string) (string, []string) {
	return "sh", []string{"-c", command}
}
