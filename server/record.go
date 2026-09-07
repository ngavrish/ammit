package main

import (
	"sort"
	"sync"

	_ "modernc.org/sqlite"
)

// The record, as a list, enforced where events land.
//
// RECORD.md is the page a client reads; this is the same list in Go, and the
// reason the page can be believed. A watchdog whose store quietly keeps
// whatever it is sent has no contract: the pipeline sends a field, nothing
// reads it, nothing says so, and a year later somebody builds a chart on a
// column that was never populated. So the list is closed. What is here is
// kept. What is not here is dropped at the door and named back to the sender
// in the same reply, which is the only moment a client is still in a position
// to do something about it.
//
// Adding a field is adding a line here and a line on the page, in one commit.

// envelope is allowed on every kind: who said it, when, and where in the run.
var envelope = []string{"kind", "at", "run", "phase", "session", "agent", "branch"}

// perKind is everything else, by the kind that carries it. Every entry is a
// field some handler, chart or judgement actually reads - the list was taken
// from the code that reads it, not from the code that sends it.
var perKind = map[string][]string{
	"run_start": {"name", "tags"},
	// usd and steps are what the runner's finish() passes as **extra at its
	// call sites: the bill and the number of steps the flow took. Named here
	// rather than waved through, because "whatever the caller happens to
	// know" is not a list.
	"run_end":       {"verdict", "summary", "seconds", "usd", "steps"},
	"flow":          {"mode", "phases"},
	"phase_start":   {},
	"phase_end":     {"seconds", "failed", "ok", "error", "text"},
	"session_start": {"model", "prefix"},
	"session_end": {"seconds", "turns", "usd", "failed", "error", "ok",
		"stopped", "model", "tokens_in", "tokens_out", "cache_read",
		"cache_write", "denied"},
	"turn": {"n", "note", "model", "request", "context", "tokens_in",
		"tokens_out", "out_est", "cache_read", "cache_write",
		"cache_write_1h", "geo"},
	"spend": {"usd", "tokens_in", "tokens_out", "cache_read", "cache_write"},
	"log":   {"level", "text", "seq"},
	"note":  {"text", "tags"},
	// A wait for the model. The request id is the session (sid#seq) and is
	// also sent as `request`, which is what a call carries to name the wait it
	// happened in, so the two join on one name.
	"request_start": {"wait", "model", "request"},
	"request_end": {"seconds", "out", "ok", "error", "detail", "wait", "model",
		"request", "msg"},
	// budget: the seconds the client will kill this item at, when it grows
	// the deadline with the work selected (a feature run by tag). Judged
	// against instead of timeouts.<kind> when larger - see itemBudget.
	"item_start": {"item", "itemkind", "budget"},
	"item_end":   {"item", "itemkind", "ok", "seconds", "failed", "error"},
	"gate":       {"verdict", "findings", "seconds"},
	// rule is which guard rule decided the call: empty when nothing
	// refused it, and a stable id like map.search-refused when something
	// did. It is the field that makes "how often was the map asked for
	// twice" a count rather than a grep over prose.
	"call":       {"tool", "input", "ok", "seconds", "why", "request", "rule"},
	"suite":      {"verdict", "total", "passed", "failed", "reason"},
	"heal_lap":   {"lap", "cap", "decision"},
	"adhoc":      {"reason", "allowed", "head"},
	"compaction": {"trigger", "fold", "pending", "rules"},
	"compression": {"comp_in", "comp_out", "dedup", "markers", "errors",
		"results"},
	"service_log": {"service", "level", "logger", "text"},
	"learning": {"item", "model", "base", "samples", "epochs", "loss",
		"minutes", "pairs", "local_rate", "sonnet_rate", "total", "fresh",
		"confirmed", "kb", "ticket", "transcripts", "funcreq"},
	// Written by this service about the machine, not by the pipeline.
	"sample":    {"container", "memory_mb", "memory_pct", "cpu_pct", "pids"},
	"netprobe":  {"host", "latency_ms", "ok"},
	"heartbeat": {},
}

// documentFields is the whole of POST /documents. The body is stored byte for
// byte; see RECORD.md on why nothing here redacts.
var documentFields = []string{"run", "kind", "phase", "body"}

var (
	droppedMu    sync.Mutex
	droppedCount = map[string]int{}
)

// recordOf returns the event as it will be kept, and the names of the fields
// that were not. An unknown kind keeps its envelope and nothing else: a kind
// this page does not list is a kind no chart reads, and storing its payload
// would be storing it for nobody.
func recordOf(e event) (event, []string) {
	allowed := map[string]bool{}
	for _, f := range envelope {
		allowed[f] = true
	}
	for _, f := range perKind[e.s("kind")] {
		allowed[f] = true
	}
	kept := event{}
	dropped := make([]string, 0, len(e))
	for key, value := range e {
		if allowed[key] {
			kept[key] = value
			continue
		}
		dropped = append(dropped, key)
	}
	if len(dropped) == 0 {
		return kept, nil
	}
	sort.Strings(dropped)
	droppedMu.Lock()
	for _, key := range dropped {
		droppedCount[e.s("kind")+"."+key]++
	}
	droppedMu.Unlock()
	return kept, dropped
}

// droppedSoFar is the running tally, so a field going nowhere is visible to
// somebody who was not watching the reply that said so.
func droppedSoFar() map[string]int {
	droppedMu.Lock()
	defer droppedMu.Unlock()
	out := make(map[string]int, len(droppedCount))
	for key, n := range droppedCount {
		out[key] = n
	}
	return out
}

// recordPage is the same contract as data, for a client that would rather ask
// than read: what the envelope carries, what each kind adds, and what has been
// turned away since this process started.
func recordPage() map[string]any {
	kinds := map[string][]string{}
	for kind, fields := range perKind {
		list := append([]string{}, fields...)
		sort.Strings(list)
		kinds[kind] = list
	}
	return map[string]any{
		"envelope":  envelope,
		"kinds":     kinds,
		"documents": documentFields,
		"dropped":   droppedSoFar(),
		"page":      "RECORD.md",
	}
}
