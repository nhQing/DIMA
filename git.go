package main

import (
	"context"
	"os/exec"
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
