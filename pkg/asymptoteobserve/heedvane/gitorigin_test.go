// Copyright (c) 2026 FlowConAI UG (haftungsbeschränkt). All rights reserved.
package heedvane

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestGitOrigin(t *testing.T) {
	t.Setenv("BEACON_DISABLE_GIT_METADATA", "")
	root := t.TempDir()
	clone := filepath.Join(root, "clone")
	sub := filepath.Join(clone, "pkg", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, clone, "init", "-q")
	gitIn(t, clone, "remote", "add", "origin", "https://x-access-token:ghp_FAKEFAKEFAKEFAKEFAKE0000@github.com/acme/widgets.git")
	gitIn(t, clone, "commit", "-q", "--allow-empty", "-m", "init")
	worktree := filepath.Join(root, "wt")
	gitIn(t, clone, "worktree", "add", "-q", worktree)
	plain := filepath.Join(root, "plain")
	noOrigin := filepath.Join(root, "noorigin")
	for _, d := range []string{plain, noOrigin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, noOrigin, "init", "-q")

	const want = "https://github.com/acme/widgets.git"
	for name, dir := range map[string]string{"clone": clone, "subfolder": sub, "linked worktree": worktree} {
		if got := GitOrigin(dir); got != want {
			t.Errorf("%s: GitOrigin = %q, want %q", name, got, want)
		}
	}
	if got := GitOrigin(plain); got != "" {
		t.Errorf("plain folder: GitOrigin = %q, want empty", got)
	}
	if got := GitOrigin(noOrigin); got != "" {
		t.Errorf("repo without origin: GitOrigin = %q, want empty", got)
	}
	if got := GitOrigin(""); got != "" {
		t.Errorf("empty cwd: GitOrigin = %q, want empty", got)
	}
	if strings.Contains(GitOrigin(clone), "ghp_") {
		t.Error("credentials leaked")
	}

	t.Setenv("BEACON_DISABLE_GIT_METADATA", "1")
	if got := GitOrigin(clone); got != "" {
		t.Errorf("disabled: GitOrigin = %q, want empty", got)
	}
}
