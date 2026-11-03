// Copyright (c) 2026 FlowConAI UG (haftungsbeschränkt). All rights reserved.
// Represented by Managing Director: Andre Machon.

package heedvane

import (
	"sync"
	"sync/atomic"
)

// SinkMaxEventBytes is the most one event may weigh when events go to a sink: the same bound the
// Heedvane helper uses for one request (24 MiB). The runtime log keeps Beacon's own 64 KiB.
const SinkMaxEventBytes = 24 << 20

var (
	sinkMu sync.RWMutex
	sink   func(line []byte) error
)

// SetEventSink sends every event the endpoint writers emit to fn, one JSON line without its newline,
// instead of appending it to the runtime log. The Heedvane helper uses it so events of projects that
// are not in Heedvane never touch a file: it reads them from the pipe, judges each one and drops the
// ones that are not its own. nil restores the log.
func SetEventSink(fn func(line []byte) error) {
	sinkMu.Lock()
	defer sinkMu.Unlock()
	sink = fn
}

// EventSink is the sink in force, or nil when events go to the runtime log.
func EventSink() func(line []byte) error {
	sinkMu.RLock()
	defer sinkMu.RUnlock()
	return sink
}

var textLimitFloor atomic.Int64

// TextLimitFloor is the length below which no retained text field is cut, 0 for Beacon's own limits
// (4,096 characters for prompts, commands and output, 2,048 for raw attributes).
func TextLimitFloor() int {
	return int(textLimitFloor.Load())
}

// SetTextLimitFloor sets the floor; 0 restores Beacon's own limits.
func SetTextLimitFloor(n int) {
	textLimitFloor.Store(int64(n))
}

// KeepEverythingLimits raises the floor for a machine set to Keep everything, which promises the
// event leaves whole: nothing a reader read is cut shorter than half of what one event may weigh.
func KeepEverythingLimits() {
	SetTextLimitFloor(SinkMaxEventBytes / 2)
}
