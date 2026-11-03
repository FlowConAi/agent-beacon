package hermessession

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
)

func TestCollectOnceMapsHermesStateDBAndAdvancesCursor(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	createHermesFixtureDB(t, dbPath)
	logPath := filepath.Join(dir, "runtime.jsonl")
	statePath := filepath.Join(dir, "hermes-state.json")

	summary, err := CollectOnce(CollectOptions{
		DBPath:    dbPath,
		StatePath: statePath,
		LogPath:   logPath,
		Write:     true,
		UserMode:  true,
	})
	if err != nil {
		t.Fatalf("CollectOnce returned error: %v", err)
	}
	if summary.Sessions != 1 || summary.SessionsChanged != 1 || summary.EventsEmitted == 0 || summary.Errors != 0 {
		t.Fatalf("summary = %#v, want one changed session with events and no errors", summary)
	}

	events := readEvents(t, logPath)
	actions := eventActions(events)
	want := []string{
		"session.started",
		"prompt.submitted",
		"agent.reasoning",
		"tool.invoked",
		"command.executed",
		"token.usage",
		"session.ended",
	}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Fatalf("actions = %v, want %v", actions, want)
	}

	command := findAction(t, events, "command.executed")
	if command.Command == nil || command.Command.Command != "go test ./..." {
		t.Fatalf("command event = %#v, want command.command", command.Command)
	}
	if command.Command.ExitCode == nil || *command.Command.ExitCode != 0 {
		t.Fatalf("exit code = %#v, want 0", command.Command.ExitCode)
	}
	if command.Harness.CollectionMethod != schema.CollectionMethodPoll {
		t.Fatalf("collection method = %q, want poll", command.Harness.CollectionMethod)
	}
	if command.Event.Fidelity != schema.FidelityObserved {
		t.Fatalf("fidelity = %q, want observed", command.Event.Fidelity)
	}

	state, err := LoadState(statePath)
	if err != nil {
		t.Fatalf("LoadState returned error: %v", err)
	}
	cursor := state.Sessions["hermes-session-1"]
	if cursor == nil || cursor.LastMessageID != 3 || !cursor.Started || cursor.EndedAtMS == 0 {
		t.Fatalf("cursor = %#v, want collected through message 3 and ended", cursor)
	}

	second, err := CollectOnce(CollectOptions{
		DBPath:    dbPath,
		StatePath: statePath,
		LogPath:   logPath,
		Write:     true,
		UserMode:  true,
	})
	if err != nil {
		t.Fatalf("second CollectOnce returned error: %v", err)
	}
	if second.EventsEmitted != 0 || second.SessionsChanged != 0 {
		t.Fatalf("second summary = %#v, want no new events", second)
	}
	if got := len(readEvents(t, logPath)); got != len(events) {
		t.Fatalf("event count after second run = %d, want %d", got, len(events))
	}
}

func TestMapSessionClassifiesFileTool(t *testing.T) {
	session := Session{ID: "s1", Model: "anthropic/claude-opus-4.6", CWD: "/repo", StartedAtMS: 1000}
	messages := []Message{
		{
			ID:          1,
			SessionID:   "s1",
			Role:        "assistant",
			TimestampMS: 2000,
			ToolCalls:   `[{"id":"call_read","function":{"name":"read_file","arguments":"{\"path\":\"README.md\"}"}}]`,
		},
		{
			ID:          2,
			SessionID:   "s1",
			Role:        "tool",
			ToolCallID:  "call_read",
			ToolName:    "read_file",
			TimestampMS: 3000,
			Content:     `{"content":"hello"}`,
		},
	}

	mapped, _ := MapSession(session, messages, &Cursor{Started: true}, nil)
	fileEvent := findMappedAction(t, mapped, "file.read")
	if fileEvent.File == nil || fileEvent.File.Path != "README.md" || fileEvent.File.Operation != "read" {
		t.Fatalf("file event = %#v, want README.md read", fileEvent.File)
	}
	if fileEvent.Model != "claude-opus-4.6" {
		t.Fatalf("model = %q, want normalized model", fileEvent.Model)
	}
}

func createHermesFixtureDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	schemaSQL := `
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    source TEXT NOT NULL,
    user_id TEXT,
    model TEXT,
    model_config TEXT,
    system_prompt TEXT,
    parent_session_id TEXT,
    started_at REAL NOT NULL,
    ended_at REAL,
    end_reason TEXT,
    message_count INTEGER DEFAULT 0,
    tool_call_count INTEGER DEFAULT 0,
    input_tokens INTEGER DEFAULT 0,
    output_tokens INTEGER DEFAULT 0,
    cache_read_tokens INTEGER DEFAULT 0,
    cache_write_tokens INTEGER DEFAULT 0,
    reasoning_tokens INTEGER DEFAULT 0,
    cwd TEXT,
    billing_provider TEXT,
    billing_base_url TEXT,
    billing_mode TEXT,
    estimated_cost_usd REAL,
    actual_cost_usd REAL,
    cost_status TEXT,
    cost_source TEXT,
    pricing_version TEXT,
    title TEXT,
    api_call_count INTEGER DEFAULT 0,
    handoff_state TEXT,
    handoff_platform TEXT,
    handoff_error TEXT,
    rewind_count INTEGER NOT NULL DEFAULT 0,
    archived INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    role TEXT NOT NULL,
    content TEXT,
    tool_call_id TEXT,
    tool_calls TEXT,
    tool_name TEXT,
    timestamp REAL NOT NULL,
    token_count INTEGER,
    finish_reason TEXT,
    reasoning TEXT,
    reasoning_content TEXT,
    reasoning_details TEXT,
    codex_reasoning_items TEXT,
    codex_message_items TEXT,
    platform_message_id TEXT,
    observed INTEGER DEFAULT 0,
    active INTEGER NOT NULL DEFAULT 1
);`
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO sessions (id, source, model, cwd, started_at, ended_at, end_reason, message_count, tool_call_count, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, estimated_cost_usd, title)
VALUES ('hermes-session-1', 'cli', 'anthropic/claude-opus-4.6', '/repo', 1780632830.5, 1780632999.25, 'cli_close', 3, 1, 100, 20, 7, 3, 5, 0.01, 'fixture');
INSERT INTO messages (id, session_id, role, content, timestamp, active)
VALUES (1, 'hermes-session-1', 'user', 'run tests', 1780632831.0, 1);
INSERT INTO messages (id, session_id, role, tool_calls, timestamp, reasoning_content, finish_reason, active)
VALUES (2, 'hermes-session-1', 'assistant', '[{"id":"call_terminal","call_id":"call_terminal","function":{"name":"terminal","arguments":"{\"command\":\"go test ./...\",\"workdir\":\"/repo\"}"}}]', 1780632832.0, 'I should run the tests.', 'tool_calls', 1);
INSERT INTO messages (id, session_id, role, tool_call_id, tool_name, content, timestamp, active)
VALUES (3, 'hermes-session-1', 'tool', 'call_terminal', 'terminal', '{"output":"ok","exit_code":0,"error":null}', 1780632833.0, 1);
`); err != nil {
		t.Fatalf("insert fixture rows: %v", err)
	}
}

func readEvents(t *testing.T, path string) []schema.Event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer f.Close()
	var out []schema.Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var ev schema.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		out = append(out, ev)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan log: %v", err)
	}
	return out
}

func eventActions(events []schema.Event) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, event.Event.Action)
	}
	return out
}

func findAction(t *testing.T, events []schema.Event, action string) schema.Event {
	t.Helper()
	for _, event := range events {
		if event.Event.Action == action {
			return event
		}
	}
	t.Fatalf("action %q not found in %v", action, eventActions(events))
	return schema.Event{}
}

func findMappedAction(t *testing.T, mapped []MappedEvent, action string) schema.Event {
	t.Helper()
	for _, item := range mapped {
		if item.Event.Event.Action == action {
			return item.Event
		}
	}
	t.Fatalf("action %q not found in mapped events", action)
	return schema.Event{}
}

// CollectOnce saves the collector state even when a session fails part way through, so the cursor
// must not move until every event in the batch is on disk. Advancing it inside the emit loop lost
// the rest of the batch: the next sweep read past those messages and never wrote them.
func TestCollectOnceKeepsCursorWhenAnEmitFails(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	createHermesFixtureDB(t, dbPath)
	statePath := filepath.Join(dir, "hermes-state.json")

	failing := &writerFailingAfter{limit: 3}
	if _, err := CollectOnce(CollectOptions{
		DBPath:    dbPath,
		StatePath: statePath,
		Print:     true,
		Out:       failing,
	}); err == nil {
		t.Fatal("CollectOnce returned no error although the writer failed mid-batch")
	}

	state, err := LoadState(statePath)
	if err != nil {
		t.Fatalf("LoadState returned error: %v", err)
	}
	if cursor := state.Sessions["hermes-session-1"]; cursor != nil {
		if cursor.LastMessageID != 0 || cursor.Started || cursor.EndedAtMS != 0 {
			t.Fatalf("cursor = %#v, want no progress recorded for a batch that was not written", cursor)
		}
	}

	logPath := filepath.Join(dir, "runtime.jsonl")
	if _, err := CollectOnce(CollectOptions{
		DBPath:    dbPath,
		StatePath: statePath,
		LogPath:   logPath,
		Write:     true,
		UserMode:  true,
	}); err != nil {
		t.Fatalf("second CollectOnce returned error: %v", err)
	}
	actions := eventActions(readEvents(t, logPath))
	want := []string{
		"session.started",
		"prompt.submitted",
		"agent.reasoning",
		"tool.invoked",
		"command.executed",
		"token.usage",
		"session.ended",
	}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Fatalf("actions after retry = %v, want the whole batch %v", actions, want)
	}
}

// Hermes writes a tool call's arguments on the assistant row and the result on a later one, which
// can land in a different sweep. The unresolved call rides on the cursor so the result is still
// classified with the command it ran.
func TestCollectOnceCarriesToolArgumentsAcrossSweeps(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	createHermesFixtureDB(t, dbPath)
	logPath := filepath.Join(dir, "runtime.jsonl")
	statePath := filepath.Join(dir, "hermes-state.json")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// Drop the tool result and the session end so the first sweep sees a call with no result yet,
	// the way a sweep that runs while Hermes is still working does.
	if _, err := db.Exec(`DELETE FROM messages WHERE id = 3;
UPDATE sessions SET ended_at = NULL, end_reason = NULL WHERE id = 'hermes-session-1';`); err != nil {
		t.Fatalf("trim fixture rows: %v", err)
	}

	opts := CollectOptions{DBPath: dbPath, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true}
	if _, err := CollectOnce(opts); err != nil {
		db.Close()
		t.Fatalf("first CollectOnce returned error: %v", err)
	}
	state, err := LoadState(statePath)
	if err != nil {
		db.Close()
		t.Fatalf("LoadState returned error: %v", err)
	}
	cursor := state.Sessions["hermes-session-1"]
	if cursor == nil || len(cursor.PendingCalls) != 1 {
		db.Close()
		t.Fatalf("cursor = %#v, want the unresolved call carried on the cursor", cursor)
	}
	// The state file holds what resumes reading, never what the session ran: the call's arguments stay in
	// Hermes's own store and are read from there on the next sweep.
	rawState, err := os.ReadFile(statePath)
	if err != nil {
		db.Close()
		t.Fatalf("read state file: %v", err)
	}
	if strings.Contains(string(rawState), "go test") || strings.Contains(string(rawState), "arguments") {
		db.Close()
		t.Fatalf("state file holds the call's arguments: %s", rawState)
	}

	if _, err := db.Exec(`
INSERT INTO messages (id, session_id, role, tool_call_id, tool_name, content, timestamp, active)
VALUES (3, 'hermes-session-1', 'tool', 'call_terminal', 'terminal', '{"output":"ok","exit_code":0,"error":null}', 1780632833.0, 1);
UPDATE sessions SET ended_at = 1780632999.25, end_reason = 'cli_close' WHERE id = 'hermes-session-1';`); err != nil {
		db.Close()
		t.Fatalf("insert tool result: %v", err)
	}
	db.Close()

	if _, err := CollectOnce(opts); err != nil {
		t.Fatalf("second CollectOnce returned error: %v", err)
	}
	command := findAction(t, readEvents(t, logPath), "command.executed")
	if command.Command == nil || command.Command.Command != "go test ./..." {
		t.Fatalf("command event = %#v, want the command from the earlier sweep's tool call", command.Command)
	}

	state, err = LoadState(statePath)
	if err != nil {
		t.Fatalf("LoadState returned error: %v", err)
	}
	if cursor := state.Sessions["hermes-session-1"]; cursor == nil || len(cursor.PendingCalls) != 0 {
		t.Fatalf("cursor = %#v, want the resolved call dropped from the cursor", cursor)
	}
}

// What the upstream collector saved after a sweep that saw the terminal call without its result: the call kept
// whole, arguments included, under pending_calls. Captured by running that collector on the fixture below.
const upstreamStateWithWaitingCall = `{
  "version": 1,
  "sessions": {
    "hermes-session-1": {
      "last_message_id": 2,
      "started": true,
      "input_tokens": 100,
      "output_tokens": 20,
      "cache_read_tokens": 7,
      "cache_write_tokens": 3,
      "reasoning_tokens": 5,
      "cost_usd": 0.01,
      "pending_calls": {
        "call_terminal": {
          "id": "call_terminal",
          "name": "terminal",
          "arguments": {
            "command": "go test ./...",
            "workdir": "/repo"
          }
        }
      }
    }
  }
}`

func execHermes(t *testing.T, dbPath, statements string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(statements); err != nil {
		t.Fatalf("exec %q: %v", statements, err)
	}
}

const removeTerminalResult = `DELETE FROM messages WHERE id = 3;
UPDATE sessions SET ended_at = NULL, end_reason = NULL WHERE id = 'hermes-session-1';`

const addTerminalResult = `INSERT INTO messages (id, session_id, role, tool_call_id, tool_name, content, timestamp, active)
VALUES (3, 'hermes-session-1', 'tool', 'call_terminal', 'terminal', '{"output":"ok","exit_code":0,"error":null}', 1780632833.0, 1);
UPDATE sessions SET ended_at = 1780632999.25, end_reason = 'cli_close' WHERE id = 'hermes-session-1';`

func requireCommandRan(t *testing.T, logPath string) {
	t.Helper()
	command := findAction(t, readEvents(t, logPath), "command.executed")
	if command.Command == nil || command.Command.Command != "go test ./..." {
		t.Fatalf("command event = %#v, want the command of the tool call", command.Command)
	}
}

func requireStateHoldsNoArguments(t *testing.T, statePath string) string {
	t.Helper()
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	for _, leaked := range []string{"go test", "workdir", "arguments", "pending_calls"} {
		if strings.Contains(string(raw), leaked) {
			t.Fatalf("state file still holds %q: %s", leaked, raw)
		}
	}
	return string(raw)
}

// A machine that ran the upstream collector holds a version 1 state with the waiting call kept whole. When the
// result is already in Hermes at the first sweep after the upgrade, the command must survive and the saved state
// must hold no arguments.
func TestCollectOnceKeepsTheCommandOfAnUpstreamCursorWhoseResultArrived(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	createHermesFixtureDB(t, dbPath)
	logPath := filepath.Join(dir, "runtime.jsonl")
	statePath := filepath.Join(dir, "hermes-state.json")
	if err := os.WriteFile(statePath, []byte(upstreamStateWithWaitingCall), 0o600); err != nil {
		t.Fatalf("write upstream state: %v", err)
	}

	if _, err := CollectOnce(CollectOptions{DBPath: dbPath, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true}); err != nil {
		t.Fatalf("CollectOnce returned error: %v", err)
	}

	requireCommandRan(t, logPath)
	requireStateHoldsNoArguments(t, statePath)
}

// The same upstream state when the result is still missing: the call is kept as a message id, the file loses its
// arguments at once, and the result that comes later is still classified with the command.
func TestCollectOnceUpgradesAnUpstreamCursorWhoseResultIsStillMissing(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	createHermesFixtureDB(t, dbPath)
	execHermes(t, dbPath, removeTerminalResult)
	logPath := filepath.Join(dir, "runtime.jsonl")
	statePath := filepath.Join(dir, "hermes-state.json")
	if err := os.WriteFile(statePath, []byte(upstreamStateWithWaitingCall), 0o600); err != nil {
		t.Fatalf("write upstream state: %v", err)
	}
	opts := CollectOptions{DBPath: dbPath, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true}

	if _, err := CollectOnce(opts); err != nil {
		t.Fatalf("first CollectOnce returned error: %v", err)
	}
	saved := requireStateHoldsNoArguments(t, statePath)
	if !strings.Contains(saved, `"call_terminal": 2`) {
		t.Fatalf("state = %s, want the waiting call kept as the id of message 2", saved)
	}

	execHermes(t, dbPath, addTerminalResult)
	if _, err := CollectOnce(opts); err != nil {
		t.Fatalf("second CollectOnce returned error: %v", err)
	}
	requireCommandRan(t, logPath)
}

// When Hermes cannot answer the lookup that upgrades an upstream cursor (here: the column it reads is missing, a
// real SQL error after the sessions were listed), the sweep fails, and the state it still saves must hold no
// arguments, for the session that failed and for one it never reached. The next sweep finds the call and the
// result is classified with its command.
func TestCollectOnceWritesNoArgumentsWhenTheUpstreamUpgradeFails(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	createHermesFixtureDB(t, dbPath)
	logPath := filepath.Join(dir, "runtime.jsonl")
	statePath := filepath.Join(dir, "hermes-state.json")
	secondSession := `"hermes-session-2": {"last_message_id": 9, "pending_calls": {"call_other": {"id": "call_other", "name": "terminal", "arguments": {"command": "echo second-session-secret"}}}},`
	state := strings.Replace(upstreamStateWithWaitingCall, `"sessions": {`, `"sessions": {`+secondSession, 1)
	if err := os.WriteFile(statePath, []byte(state), 0o600); err != nil {
		t.Fatalf("write upstream state: %v", err)
	}
	opts := CollectOptions{DBPath: dbPath, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true}

	execHermes(t, dbPath, `ALTER TABLE messages RENAME COLUMN tool_calls TO tool_calls_away;`)
	if _, err := CollectOnce(opts); err == nil {
		t.Fatal("CollectOnce succeeded although the lookup of the waiting call failed")
	}
	saved := requireStateHoldsNoArguments(t, statePath)
	if strings.Contains(saved, "second-session-secret") {
		t.Fatalf("state file holds the other session's arguments: %s", saved)
	}
	if !strings.Contains(saved, "call_terminal") {
		t.Fatalf("state = %s, want the waiting call kept by its id so the next sweep can retry", saved)
	}

	execHermes(t, dbPath, `ALTER TABLE messages RENAME COLUMN tool_calls_away TO tool_calls;`)
	if _, err := CollectOnce(opts); err != nil {
		t.Fatalf("CollectOnce after Hermes answers again returned error: %v", err)
	}
	requireCommandRan(t, logPath)
	requireStateHoldsNoArguments(t, statePath)
}

// Hermes compaction keeps a compacted row with active = 0. A call whose message was compacted between two sweeps
// still has its arguments there, and its result must be classified with them.
func TestCollectOnceReadsAWaitingCallThatHermesArchivedBetweenSweeps(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	createHermesFixtureDB(t, dbPath)
	execHermes(t, dbPath, removeTerminalResult)
	logPath := filepath.Join(dir, "runtime.jsonl")
	statePath := filepath.Join(dir, "hermes-state.json")
	opts := CollectOptions{DBPath: dbPath, StatePath: statePath, LogPath: logPath, Write: true, UserMode: true}

	if _, err := CollectOnce(opts); err != nil {
		t.Fatalf("first CollectOnce returned error: %v", err)
	}
	execHermes(t, dbPath, `UPDATE messages SET active = 0 WHERE id = 2;`)
	execHermes(t, dbPath, addTerminalResult)
	if _, err := CollectOnce(opts); err != nil {
		t.Fatalf("second CollectOnce returned error: %v", err)
	}

	requireCommandRan(t, logPath)
}

type writerFailingAfter struct {
	limit  int
	writes int
}

func (w *writerFailingAfter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes > w.limit {
		return 0, errors.New("destination unavailable")
	}
	return len(p), nil
}
