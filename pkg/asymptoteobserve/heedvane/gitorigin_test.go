// Copyright (c) 2026 FlowConAI UG (haftungsbeschränkt). All rights reserved.
package heedvane

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Each case was compared with `git config --file <config> remote.origin.url`.
func TestGitOriginReadsConfigAsGitDoes(t *testing.T) {
	t.Setenv("BEACON_DISABLE_GIT_METADATA", "")
	const want = "https://github.com/acme/widgets.git"
	for name, config := range map[string]string{
		"another remote in other case first": "[remote \"Origin\"]\n\turl = https://github.com/acme/other.git\n[remote \"origin\"]\n\turl = " + want + "\n",
		"section name in other case":         "[REMOTE \"origin\"]\n\turl = " + want + "\n",
		"comment after the header":           "[remote \"origin\"] # main remote\n\turl = " + want + "\n",
		"comment after the value":            "[remote \"origin\"]\n\turl = " + want + " ; main remote\n",
		"quoted value":                       "[remote \"origin\"]\n\turl = \"" + want + "\"\n",
	} {
		dir := writeConfig(t, config)
		if out, err := exec.Command("git", "config", "--file", filepath.Join(dir, ".git", "config"), "remote.origin.url").Output(); err != nil || strings.TrimSpace(string(out)) != want {
			t.Errorf("%s: git itself reads %q (%v)", name, out, err)
		}
		if got := GitOrigin(dir); got != want {
			t.Errorf("%s: GitOrigin = %q, want %q", name, got, want)
		}
	}
}

func TestGitOriginNoticesAChangedOrigin(t *testing.T) {
	t.Setenv("BEACON_DISABLE_GIT_METADATA", "")
	dir := t.TempDir()
	if got := GitOrigin(dir); got != "" {
		t.Fatalf("no repository yet: GitOrigin = %q", got)
	}
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	const want = "https://github.com/acme/widgets.git"
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[remote \"origin\"]\n\turl = "+want+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	gitOriginCache.Store(resolved, cachedOrigin{origin: "", at: time.Now().Add(-gitOriginFreshFor - time.Second)})
	if got := GitOrigin(dir); got != want {
		t.Errorf("after the answer aged out: GitOrigin = %q, want %q", got, want)
	}
}

func TestSanitizeRemoteURLKeepsNoCredentials(t *testing.T) {
	for raw, want := range map[string]string{
		"https://git.example/repo%ZZ?private_token=SECRET": "",
		"https://user:SECRET@git.example/repo%ZZ?t=SECRET": "",
		"https://user:SECRET@github.com/acme/widgets.git":  "https://github.com/acme/widgets.git",
		"https://github.com/acme/widgets.git?t=SECRET":     "https://github.com/acme/widgets.git",
		"git@github.com:acme/widgets.git?t=SECRET":         "github.com:acme/widgets.git",
	} {
		if got := SanitizeRemoteURL(raw); got != want {
			t.Errorf("SanitizeRemoteURL(%q) = %q, want %q", raw, got, want)
		}
	}
}
