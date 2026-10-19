package main

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// GitInfo is what the source folder's repository said at the moment of a
// build. A tag alone cannot answer "which branch did this image come from"
// when several branches are in flight, which is exactly when the question
// gets asked.
type GitInfo struct {
	IsRepo bool   `json:"isRepo"`
	Branch string `json:"branch"`
	Commit string `json:"commit"`
	// Dirty means the working tree had uncommitted changes, so this image
	// matches no commit at all and cannot be rebuilt from the repository.
	Dirty bool `json:"dirty"`
}

// DetachedBranch is what git reports when the checkout is not on a branch.
const DetachedBranch = "HEAD"

// gitIn runs one git command inside a folder and returns its trimmed output.
func gitIn(dir string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// Commit is one line of a change list.
type Commit struct {
	Hash    string `json:"hash"`
	Author  string `json:"author"`
	Date    string `json:"date"`
	Subject string `json:"subject"`
}

// maxChanges keeps a change list readable; a gap wider than this between two
// releases is better read in a real git client.
const maxChanges = 100

// CommitsBetween lists the commits reachable from to but not from from —
// what a release at `to` contains that the one at `from` did not. An empty
// from lists the newest commits up to `to`.
//
// The commits have to exist in this clone. A build made on another machine,
// or from a branch since deleted, may name a commit this folder has never
// seen, and that is reported rather than shown as "no changes".
func CommitsBetween(dir, from, to string) ([]Commit, error) {
	if _, ok := gitIn(dir, "cat-file", "-e", to+"^{commit}"); !ok {
		return nil, fmt.Errorf("thư mục mã nguồn không có commit %s — có thể bản này build ở máy khác, hoặc nhánh đã bị xoá; chạy git fetch rồi thử lại", to)
	}
	rng := to
	if from != "" {
		if _, ok := gitIn(dir, "cat-file", "-e", from+"^{commit}"); !ok {
			return nil, fmt.Errorf("thư mục mã nguồn không có commit %s của bản trước — chạy git fetch rồi thử lại", from)
		}
		rng = from + ".." + to
	}
	out, ok := gitIn(dir, "log", "--no-decorate", "--date=short",
		"--format=%h%x09%an%x09%ad%x09%s", "-n", strconv.Itoa(maxChanges), rng)
	if !ok {
		return nil, fmt.Errorf("git log lỗi trong %s", dir)
	}
	list := []Commit{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, "\t", 4)
		if len(f) == 4 {
			list = append(list, Commit{Hash: f[0], Author: f[1], Date: f[2], Subject: f[3]})
		}
	}
	return list, nil
}

// ReadGit inspects a build context folder.
//
// A folder that is not a repository, a repository without commits, or a
// machine without git all yield IsRepo false. This is extra context about a
// build, never a reason to refuse one.
func ReadGit(dir string) GitInfo {
	if strings.TrimSpace(dir) == "" {
		return GitInfo{}
	}
	branch, ok := gitIn(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if !ok || branch == "" {
		return GitInfo{}
	}
	info := GitInfo{IsRepo: true, Branch: branch}
	if commit, ok := gitIn(dir, "rev-parse", "--short", "HEAD"); ok {
		info.Commit = commit
	}
	// --porcelain prints one line per changed path and nothing at all when
	// the tree is clean.
	if status, ok := gitIn(dir, "status", "--porcelain"); ok {
		info.Dirty = status != ""
	}
	return info
}
