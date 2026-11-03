// Copyright (c) 2026 FlowConAI UG (haftungsbeschränkt). All rights reserved.
// Represented by Managing Director: Andre Machon.

package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/heedvane"
)

// `beacon heedvane sync` is the one command the Heedvane helper runs. It reads every agent's own
// session store the way `beacon endpoint backfill` does (the same runtime list, so a reader added
// upstream is read here too) and writes each event as one JSON line to standard output instead of to
// the runtime log: the helper judges every line on the machine, so an event of a project that is not
// in Heedvane is never written to a file. Cursors move as each reader finishes and live under
// --state-dir, never under ~/.beacon. The helper hands in a copy of its cursors and keeps the new
// ones only after it has queued every line, so a crash reads the same events again; Heedvane drops
// copies by event id.
var heedvaneSyncOpts struct {
	stateDir       string
	skip           string
	keepEverything bool
}

var heedvaneCmd = &cobra.Command{
	Use:    "heedvane",
	Short:  "Commands the Heedvane helper runs",
	Hidden: true,
}

var heedvaneSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Write every agent's new session events to standard output",
	Long: `Reads each agent's local session store from its cursor and writes the new events, one JSON
line each, to standard output. A one-line JSON summary goes to standard error at the end.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runHeedvaneSync,
}

func init() {
	heedvaneSyncCmd.Flags().StringVar(&heedvaneSyncOpts.stateDir, "state-dir", "", "Directory for the readers' cursor files (required)")
	heedvaneSyncCmd.Flags().StringVar(&heedvaneSyncOpts.skip, "skip", "", "Comma-separated readers not to run, by their command name (claude, codex, ...)")
	heedvaneSyncCmd.Flags().BoolVar(&heedvaneSyncOpts.keepEverything, "keep-everything", false, "Cut no text field short (a machine set to Keep everything)")
	heedvaneCmd.AddCommand(heedvaneSyncCmd)
	rootCmd.AddCommand(heedvaneCmd)
}

// heedvaneReaderKey is the command word a runner is known by: "claude" for "beacon endpoint claude sync".
func heedvaneReaderKey(sync string) string {
	words := strings.Fields(strings.TrimPrefix(sync, "beacon endpoint "))
	if len(words) == 0 {
		return ""
	}
	return words[0]
}

type heedvaneRuntimeSummary struct {
	Name   string `json:"name"`
	Key    string `json:"key"`
	Events int    `json:"events"`
	Error  string `json:"error,omitempty"`
}

type heedvaneSyncSummary struct {
	Events   int                      `json:"events"`
	Runtimes []heedvaneRuntimeSummary `json:"runtimes"`
	Seconds  float64                  `json:"seconds"`
}

func runHeedvaneSync(cmd *cobra.Command, _ []string) error {
	if strings.TrimSpace(heedvaneSyncOpts.stateDir) == "" {
		return fmt.Errorf("--state-dir is required")
	}
	if err := os.MkdirAll(heedvaneSyncOpts.stateDir, 0o700); err != nil {
		return err
	}
	summary, err := heedvaneSync(cmd.OutOrStdout(), heedvaneSyncOpts.stateDir, heedvaneSkipSet(heedvaneSyncOpts.skip), heedvaneSyncOpts.keepEverything)
	data, marshalErr := json.Marshal(summary)
	if marshalErr == nil {
		fmt.Fprintln(cmd.ErrOrStderr(), string(data))
	}
	return err
}

func heedvaneSkipSet(skip string) map[string]bool {
	set := map[string]bool{}
	for _, key := range strings.Split(skip, ",") {
		if key = strings.TrimSpace(key); key != "" {
			set[key] = true
		}
	}
	return set
}

// heedvaneSync runs every reader that is not skipped, with events going to out. A reader that fails
// is named in the summary and the others still run; a failed write to out stops the whole run,
// because nobody is reading any more and no cursor may move past what was not read.
func heedvaneSync(out io.Writer, stateDir string, skip map[string]bool, keepEverything bool) (heedvaneSyncSummary, error) {
	started := time.Now()
	var mu sync.Mutex
	buffered := bufio.NewWriterSize(out, 1<<20)
	events := 0
	var writeErr error

	heedvane.SetEventSink(func(line []byte) error {
		mu.Lock()
		defer mu.Unlock()
		if writeErr != nil {
			return writeErr
		}
		if _, err := buffered.Write(line); err != nil {
			writeErr = err
			return err
		}
		if err := buffered.WriteByte('\n'); err != nil {
			writeErr = err
			return err
		}
		events++
		return nil
	})
	defer heedvane.SetEventSink(nil)
	if keepEverything {
		heedvane.KeepEverythingLimits()
	}

	summary := heedvaneSyncSummary{Runtimes: []heedvaneRuntimeSummary{}}
	run := endpointBackfillRun{LogPath: filepath.Join(stateDir, "unused-runtime.jsonl"), UserMode: true, StateDir: stateDir}
	for _, runner := range endpointBackfillRunners() {
		key := heedvaneReaderKey(runner.Sync)
		if skip[key] {
			continue
		}
		before := events
		_, err := runner.Run(run)
		flushErr := buffered.Flush()
		runtime := heedvaneRuntimeSummary{Name: runner.Name, Key: key, Events: events - before}
		if err != nil {
			runtime.Error = err.Error()
		}
		if flushErr != nil {
			writeErr = flushErr
		}
		if runtime.Events > 0 || runtime.Error != "" {
			summary.Runtimes = append(summary.Runtimes, runtime)
		}
		if writeErr != nil {
			break
		}
	}
	summary.Events = events
	summary.Seconds = time.Since(started).Seconds()
	return summary, writeErr
}
