package writer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/schema"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/heedvane"
)

func withSink(t *testing.T) *[][]byte {
	t.Helper()
	var lines [][]byte
	heedvane.SetEventSink(func(line []byte) error {
		lines = append(lines, append([]byte(nil), line...))
		return nil
	})
	t.Cleanup(func() { heedvane.SetEventSink(nil) })
	return &lines
}

func promptEvent(text string) schema.Event {
	event := schema.NewEvent(schema.NewEventOptions{Action: "prompt.submitted", Harness: schema.HarnessInfo{Name: "test"}, Message: "prompt"})
	event.Prompt = &asymptoteobserve.PromptInfo{Text: text}
	return event
}

func TestAppendEventGoesToTheSinkAndNeverToTheLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	lines := withSink(t)

	if _, err := AppendEvent(promptEvent("hello"), Options{Path: path}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if len(*lines) != 1 {
		t.Fatalf("sink got %d lines, want 1", len(*lines))
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal((*lines)[0], &decoded); err != nil || decoded["vendor"] != schema.Vendor {
		t.Fatalf("sink line is not an event: %v %s", err, (*lines)[0])
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the runtime log was written (stat err %v): an event of a project that is not in Heedvane must touch no file", err)
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("the log lock was created (stat err %v)", err)
	}
}

func TestSinkEventsKeepTheSecretRedaction(t *testing.T) {
	lines := withSink(t)
	event := promptEvent("hello")
	event.Message = "token=super-secret-value"

	if _, err := AppendEvent(event, Options{}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if strings.Contains(string((*lines)[0]), "super-secret-value") {
		t.Fatalf("secret reached the sink: %s", (*lines)[0])
	}
}

func TestSinkEventOverTheLogLimitLeavesWholeUnderKeepEverything(t *testing.T) {
	lines := withSink(t)
	big := strings.Repeat("a long prompt line. ", 12_000) // 240,000 characters, far past 64 KiB and the 4,096 field limit

	if _, err := AppendEvent(promptEvent(big), Options{}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	var cut asymptoteobserve.Event
	if err := json.Unmarshal((*lines)[0], &cut); err != nil || cut.Prompt == nil || len(cut.Prompt.Text) >= len(big) {
		t.Fatalf("with Beacon's own limits the prompt should be cut at 4,096 characters, got %d (err %v)", len(cut.Prompt.Text), err)
	}

	heedvane.KeepEverythingLimits()
	t.Cleanup(func() { heedvane.SetTextLimitFloor(0) })
	if _, err := AppendEvent(promptEvent(big), Options{}); err != nil {
		t.Fatalf("AppendEvent under Keep everything: %v", err)
	}
	var whole asymptoteobserve.Event
	if err := json.Unmarshal((*lines)[1], &whole); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if whole.Prompt == nil || whole.Prompt.Text != big || whole.Truncated {
		t.Fatalf("Keep everything cut the prompt: %d of %d characters, truncated=%v", len(whole.Prompt.Text), len(big), whole.Truncated)
	}
}

func TestASinkWriteErrorReachesTheReader(t *testing.T) {
	heedvane.SetEventSink(func([]byte) error { return os.ErrClosed })
	t.Cleanup(func() { heedvane.SetEventSink(nil) })

	if _, err := AppendEvent(promptEvent("hello"), Options{}); err != os.ErrClosed {
		t.Fatalf("AppendEvent error = %v, want the sink's error so a reader stops and keeps its cursor", err)
	}
}
