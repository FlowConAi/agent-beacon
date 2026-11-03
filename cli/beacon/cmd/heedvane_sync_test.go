package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stateWritingRunner is writingRunner that also leaves its cursor file where the run says it belongs.
func stateWritingRunner(name, sync string, events int, seenState *[]string) endpointBackfillRunner {
	inner := writingRunner(name, events, nil)
	inner.Sync = sync
	run := inner.Run
	inner.Run = func(r endpointBackfillRun) (int, error) {
		cursor := r.statePath(filepath.Join(os.TempDir(), "default-state", strings.ToLower(name)+"-sessions.json"))
		*seenState = append(*seenState, cursor)
		if err := os.MkdirAll(filepath.Dir(cursor), 0o700); err != nil {
			return 0, err
		}
		if err := os.WriteFile(cursor, []byte("{}"), 0o600); err != nil {
			return 0, err
		}
		return run(r)
	}
	return inner
}

func TestHeedvaneSyncWritesEventsToTheOutputKeepsCursorsUnderTheStateDirAndSkipsNamedReaders(t *testing.T) {
	stubBackfill(t, 1<<30, 1<<30)
	var cursors []string
	endpointBackfillRunners = func() []endpointBackfillRunner {
		return []endpointBackfillRunner{
			stateWritingRunner("Claude", "beacon endpoint claude sync", 3, &cursors),
			stateWritingRunner("OpenCode", "beacon endpoint opencode sync", 2, &cursors),
			stateWritingRunner("Pi", "beacon endpoint pi sync", 4, &cursors),
		}
	}
	stateDir := filepath.Join(t.TempDir(), "state")
	var out bytes.Buffer

	summary, err := heedvaneSync(&out, stateDir, heedvaneSkipSet("claude, codex"), false)
	if err != nil {
		t.Fatalf("heedvaneSync: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 6 || summary.Events != 6 {
		t.Fatalf("got %d output lines and summary.Events=%d, want 6 (OpenCode 2 and Pi 4; Claude is skipped)", len(lines), summary.Events)
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "{") || strings.Contains(line, "claude") {
			t.Fatalf("a skipped reader's event, or a line that is not JSON, reached the output: %s", line)
		}
	}
	if len(summary.Runtimes) != 2 || summary.Runtimes[0].Key != "opencode" || summary.Runtimes[0].Events != 2 || summary.Runtimes[1].Key != "pi" || summary.Runtimes[1].Events != 4 {
		t.Fatalf("summary = %#v", summary.Runtimes)
	}
	for _, cursor := range cursors {
		if filepath.Dir(cursor) != stateDir {
			t.Fatalf("a cursor was kept at %s, outside --state-dir %s", cursor, stateDir)
		}
	}
	if len(cursors) != 2 {
		t.Fatalf("readers run = %v, want OpenCode and Pi only", cursors)
	}
	leftover, _ := filepath.Glob(filepath.Join(stateDir, "*.jsonl*"))
	if len(leftover) != 0 {
		t.Fatalf("a runtime log was written under the state dir: %v", leftover)
	}
}

func TestHeedvaneSyncNamesAReaderThatFailsAndRunsTheOthers(t *testing.T) {
	stubBackfill(t, 1<<30, 1<<30)
	var cursors []string
	broken := endpointBackfillRunner{Name: "Cursor", Sync: "beacon endpoint cursor sync", Run: func(endpointBackfillRun) (int, error) { return 0, errors.New("state.vscdb is locked") }}
	endpointBackfillRunners = func() []endpointBackfillRunner {
		return []endpointBackfillRunner{broken, stateWritingRunner("Pi", "beacon endpoint pi sync", 1, &cursors)}
	}
	var out bytes.Buffer

	summary, err := heedvaneSync(&out, t.TempDir(), nil, false)
	if err != nil {
		t.Fatalf("a reader that fails must not fail the run: %v", err)
	}
	if summary.Events != 1 || len(summary.Runtimes) != 2 || summary.Runtimes[0].Error != "state.vscdb is locked" {
		t.Fatalf("summary = %#v", summary)
	}
}

type brokenPipe struct{}

func (brokenPipe) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestHeedvaneSyncStopsWhenNobodyReadsTheOutput(t *testing.T) {
	stubBackfill(t, 1<<30, 1<<30)
	var cursors []string
	endpointBackfillRunners = func() []endpointBackfillRunner {
		return []endpointBackfillRunner{
			stateWritingRunner("OpenCode", "beacon endpoint opencode sync", 5, &cursors),
			stateWritingRunner("Pi", "beacon endpoint pi sync", 5, &cursors),
		}
	}

	_, err := heedvaneSync(brokenPipe{}, t.TempDir(), nil, false)
	if err == nil {
		t.Fatal("a closed output must fail the run, so the helper keeps its old cursors")
	}
	if len(cursors) != 1 {
		t.Fatalf("readers started after the output closed: %v", cursors)
	}
}

func TestHeedvaneSyncNeedsAStateDir(t *testing.T) {
	if err := runHeedvaneSync(heedvaneSyncCmd, nil); err == nil || !strings.Contains(err.Error(), "--state-dir") {
		t.Fatalf("running without --state-dir = %v", err)
	}
}
