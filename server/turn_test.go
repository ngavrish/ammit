package main

import (
	"testing"
	"time"
)

// A session's silence is excused only by a test that is running now. The
// reporting phase of APF-3296 run 58344b33 had no test open and hung for 53
// minutes on a tool; the runner's CPU said "busy" and the turn limit never fired.
func TestSilenceIsExcusedOnlyByARunningTest(t *testing.T) {
	if err := openDB(t.TempDir() + "/ammit.db"); err != nil {
		t.Fatal(err)
	}
	const run = "58344b33-6721-4767-8c1f-f05564254350"
	conf := Config{"timeouts": {"module": "1200"}}
	now := float64(time.Now().UnixNano()) / 1e9
	insert := func(at float64, kind, session string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO events (at, kind, run, session, agent, payload) VALUES (?,?,?,?,?,?)`,
			at, kind, run, session, "suite", `{}`); err != nil {
			t.Fatal(err)
		}
	}

	if testRunning(conf, run) {
		t.Fatal("no test was ever opened, yet a test is running")
	}
	insert(now-60, "item_start", "configuration_modal_toggle_all")
	if !testRunning(conf, run) {
		t.Fatal("a test opened a minute ago and not closed is not counted as running")
	}
	insert(now-30, "item_end", "configuration_modal_toggle_all")
	if testRunning(conf, run) {
		t.Fatal("a closed test still counts as running")
	}
	insert(now-5000, "item_start", "killed_feature_never_closed")
	if testRunning(conf, run) {
		t.Fatal("a test opened 5000s ago and never closed keeps excusing silence past timeouts.module")
	}
}
