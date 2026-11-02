//go:build !windows

package beaconjsonexporter

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/asymptote-labs/agent-beacon/collector-builder/exporter/beaconjsonexporter/internal/beaconevent"
)

// The collector runs elevated in a system-mode install and creates the runtime log, while hooks
// running as the console user append to the same file. A restrictive umask would leave the hooks
// unable to write, and telemetry would stop while the collector looked healthy.
//
// Unix-only because umask is. On Windows the equivalent property is an ACL on the system log
// directory, which is a real gap there rather than a non-issue.

func TestAppendJSONLCreatesSharedRuntimeFilesDespiteUmask(t *testing.T) {
	oldUmask := syscall.Umask(0022)
	t.Cleanup(func() {
		syscall.Umask(oldUmask)
	})

	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := appendJSONL(path, []byte(`{"message":"test"}`+"\n"), defaultRotateBytes, defaultRotateArchives); err != nil {
		t.Fatalf("appendJSONL returned error: %v", err)
	}

	for _, target := range []string{path, path + ".lock"} {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatalf("stat %s: %v", target, err)
		}
		if got := info.Mode().Perm(); got != runtimeFileMode {
			t.Fatalf("%s mode = %o, want %o", target, got, runtimeFileMode)
		}
	}
}

// An OTLP event names its folder like a hook event does, so it names the folder's repository too.
func TestAppendStampsGitOrigin(t *testing.T) {
	t.Setenv("BEACON_DISABLE_GIT_METADATA", "")
	clone := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "https://x-access-token:FAKE@github.com/acme/widgets.git"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = clone
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	event := beaconEvent{Session: &beaconevent.SessionInfo{ID: "s", WorkingDirectory: clone}}
	writer := jsonlWriter{path: path, maxEventBytes: 1 << 20, rotateBytes: defaultRotateBytes, rotateArchives: defaultRotateArchives}
	if err := writer.append(event); err != nil {
		t.Fatalf("append: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var line struct {
		GitOrigin string `json:"git_origin"`
	}
	if err := json.Unmarshal(data, &line); err != nil {
		t.Fatal(err)
	}
	if line.GitOrigin != "https://github.com/acme/widgets.git" {
		t.Errorf("git_origin = %q", line.GitOrigin)
	}
}
