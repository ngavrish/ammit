package main

import "testing"

// The ids and times below are read off the record of the APF-3296 chain that
// began with run db62b413 (bugsrules, limit:loops.laps_bugsfix).
const (
	wdTestDropped = "db62b413-341d-4a64-beb0-6bb9217b908f"
	wdTestReplay  = "0dd68e70-cc08-4a0b-8998-4e7d6b6ac554"
)

// A run that reports its start while the start command is still running is
// tied to the queue row that started it. WD-db62b413-2 (queue row 60) became
// run e8b5fb60, whose run_start is stamped before the row's started: the row
// was still waiting when run_start looked for it, stayed running with no run,
// and the watchdog failed the cycle as "started and no run came of it".
func TestQueueRowIsTiedToARunThatStartsInsideTheStartCommand(t *testing.T) {
	if err := openDB(t.TempDir() + "/ammit.db"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO queue (name, payload, requested) VALUES (?,?,?)`,
		"WD-db62b413-2", "{}", 1791692227.1876311); err != nil {
		t.Fatal(err)
	}
	saved := queueStart
	defer func() { queueStart = saved }()
	queueStart = func(_ string, _ Config, ctx map[string]string) string {
		store(event{"kind": "run_start", "run": "e8b5fb60-7df9-45f3-acbe-d25d4de53da3",
			"name": ctx["name"], "at": 1791696276.8886657})
		return "started"
	}

	pumpQueue(Config{})

	var run, state string
	if err := db.QueryRow(`SELECT coalesce(run,''), state FROM queue WHERE name=?`,
		"WD-db62b413-2").Scan(&run, &state); err != nil {
		t.Fatal(err)
	}
	if run != "e8b5fb60-7df9-45f3-acbe-d25d4de53da3" || state != "running" {
		t.Fatalf("queue row is %q with run %q, want running with run e8b5fb60", state, run)
	}
}

// A dropped run resumed while its cycle is still fixing is the cycle's
// resume. db62b413 dropped at bugsrules; #624 fixed the gate and 0dd68e70
// replayed it from bugsrules (green there) while cycle 1 was fixing. The
// cycle did not see it and spent three cycles on a drop that was past, and
// 0dd68e70's own drop at reportrules began a second chain beside this one.
func TestFixingCycleFollowsAResumeStartedOutsideIt(t *testing.T) {
	if err := openDB(t.TempDir() + "/ammit.db"); err != nil {
		t.Fatal(err)
	}
	mustExec := func(q string, args ...any) int64 {
		t.Helper()
		res, err := db.Exec(q, args...)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	mustExec(`INSERT INTO watchdog_drops (run, root, name, mode, phase, cause, verdict, evidence, at)
	          VALUES (?,?,?,?,?,?,?,?,?)`, wdTestDropped, wdTestDropped, "APF-3296",
		"functional-tester", "bugsrules", "limit:loops.laps_bugsfix", "BLOCKED", "[]", 1791687016.2542412)
	fix := mustExec(`INSERT INTO queue (name, payload, requested) VALUES (?,?,?)`,
		"WD-db62b413-1", "{}", 1791687016.2635043)
	cycle := mustExec(`INSERT INTO watchdog_cycles (root, run, cycle, state, branch, fix_queue,
	                     started, entered, updated) VALUES (?,?,?,?,?,?,?,?,?)`,
		wdTestDropped, wdTestDropped, 1, wdFixing, "watchdog/db62b413-1", fix,
		1791687016.2617807, 1791687016.2617807, 1791687016.2617807)
	// A replay of another run, and one that predates the cycle, are not it.
	mustExec(`INSERT INTO runs (run, name, started) VALUES (?,?,?)`, "older", "APF-3296", 1791686000.0)
	mustExec(`INSERT INTO events (at, kind, run, payload) VALUES (?, 'replay', ?, ?)`,
		1791686000.0, "older", `{"source_run":"`+wdTestDropped+`","from_phase":"bugsrules"}`)
	mustExec(`INSERT INTO runs (run, name, started) VALUES (?,?,?)`, "other", "APF-3296", 1791687100.0)
	mustExec(`INSERT INTO events (at, kind, run, payload) VALUES (?, 'replay', ?, ?)`,
		1791687100.0, "other", `{"source_run":"f66b6ca8","from_phase":"bugsrules"}`)

	step := func() wdCycle {
		t.Helper()
		cs := wdCycles(`id=?`, cycle)
		if len(cs) != 1 {
			t.Fatalf("cycle %d not found", cycle)
		}
		stepCycle(cs[0], Config{}, 3)
		return wdCycles(`id=?`, cycle)[0]
	}

	if c := step(); c.state != wdFixing {
		t.Fatalf("with no replay of %s the cycle went to %s, want it still fixing", wdTestDropped, c.state)
	}

	mustExec(`INSERT INTO runs (run, name, started) VALUES (?,?,?)`, wdTestReplay, "APF-3296",
		1791687393.4891937)
	mustExec(`INSERT INTO events (at, kind, run, payload) VALUES (?, 'replay', ?, ?)`,
		1791687393.553098, wdTestReplay, `{"source_run":"`+wdTestDropped+`","from_phase":"bugsrules"}`)

	c := step()
	if c.state != wdResumed || c.resumedRun != wdTestReplay {
		t.Fatalf("cycle is %s resuming %q, want %s resuming %s", c.state, c.resumedRun, wdResumed, wdTestReplay)
	}
	var queued string
	if err := db.QueryRow(`SELECT state FROM queue WHERE id=?`, fix).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != "done" {
		t.Errorf("the fix run still waiting to start is %s, want withdrawn (done)", queued)
	}

	// The adopted resume's own drop is the same chain's, not a new one.
	if root, parent := rootOf(wdTestReplay); root != wdTestDropped || parent != cycle {
		t.Errorf("rootOf(%s) = %s, cycle %d; want %s, cycle %d", wdTestReplay, root, parent,
			wdTestDropped, cycle)
	}
	if root, parent := rootOf("unrelated"); root != "unrelated" || parent != 0 {
		t.Errorf("rootOf(unrelated) = %s, cycle %d; want itself, 0", root, parent)
	}
}
