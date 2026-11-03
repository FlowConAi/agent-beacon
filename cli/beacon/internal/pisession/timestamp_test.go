package pisession

import "testing"

// A session header dated with an ISO timestamp used to read as the number 2026 and date the session
// 33 minutes after the epoch; measured on 72 of the 2,442 events of one machine's real Pi history.
func TestSessionStartedKeepsTheHeadersIsoTimestamp(t *testing.T) {
	ref := SessionRef{ID: "s", Path: "/tmp/s.jsonl", Header: map[string]interface{}{"type": "session", "id": "s", "cwd": "/work", "timestamp": "2026-03-08T20:06:49.123Z"}}

	mapped := MapSession(ref, []Entry{{Line: 1, Data: ref.Header}}, MapOptions{})
	if len(mapped) == 0 || mapped[0].Event.Event.Action != "session.started" {
		t.Fatalf("mapped = %#v", actionsOf(mapped))
	}
	if got, want := mapped[0].Event.Timestamp, "2026-03-08T20:06:49.123000000Z"; got != want {
		t.Fatalf("session.started timestamp = %q, want %q", got, want)
	}
}

func TestFloatValueFromStringReadsOnlyAWholeNumber(t *testing.T) {
	for in, want := range map[string]int64{"1772999209": 1772999209, " 1772999209123 ": 1772999209123} {
		if got, ok := floatValueFromString(in); !ok || int64(got) != want {
			t.Fatalf("floatValueFromString(%q) = %v, %v", in, got, ok)
		}
	}
	if _, ok := floatValueFromString("2026-03-08T20:06:49Z"); ok {
		t.Fatal("an ISO timestamp is not a number")
	}
}
