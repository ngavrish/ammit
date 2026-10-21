package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// loadRunRecord writes the record of run 65a6d56e (APF-3296), as ammit holds
// it, into the test database: its runs row, its flow and every phase_start,
// phase_end, gate, replay, run_start and run_end event it reported.
func loadRunRecord(t *testing.T, run string) {
	t.Helper()
	flow, err := os.ReadFile("testdata/watchdog_65a6d56e_flow.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/watchdog_65a6d56e_events.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows [][]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO runs (run, name, started, finished, verdict, summary)
	                      VALUES (?,?,?,?,?,?)`, run, "APF-3296", 1791988110.009336,
		1791988557.9482317, "UNKNOWN", "[exit 0]"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO flows (run, at, mode, phases) VALUES (?,?,?,?)`,
		run, 1791988110.0, "functional-tester", string(flow)); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		kind, phase, branch := r[1].(string), r[2].(string), r[3].(string)
		p := map[string]any{"kind": kind, "run": run, "phase": phase}
		switch kind {
		case "gate", "run_end":
			p["verdict"] = r[4]
		case "replay":
			p["from_phase"] = r[5]
		case "run_start":
			p["name"] = "APF-3296"
			p["tags"] = map[string]string{"mode": "functional-tester"}
		}
		b, _ := json.Marshal(p)
		if _, err := db.Exec(`INSERT INTO events (at, kind, run, phase, branch, payload)
		                      VALUES (?,?,?,?,?,?)`, r[0], kind, run, phase, branch, string(b)); err != nil {
			t.Fatal(err)
		}
	}
}

// A run that got through every phase and still dropped names a phase to go
// again from. 65a6d56e replayed APF-3296 from foldgate, went through all 42
// phases and ended UNKNOWN on a push publish had refused; its drop was
// recorded with no phase, the resume asked for one with no from_phase, and
// the orchestrator refused it: "got through all 42 phases, so there is
// nothing to resume: name the phase to go again from". Its own work began at
// foldgate, so that is where it goes again from.
func TestRunThroughEveryPhaseResumesFromWhereItsWorkBegan(t *testing.T) {
	const run = "65a6d56e-5ed9-4b35-8960-dc2d5dcc10ca"
	if err := openDB(t.TempDir() + "/ammit.db"); err != nil {
		t.Fatal(err)
	}
	loadRunRecord(t, run)
	f, err := readRunFacts(run)
	if err != nil {
		t.Fatal(err)
	}
	if got := frontierFrom(f); got != "" {
		t.Fatalf("frontierFrom = %q; the record should show a run through every phase", got)
	}
	d, why := classifyDrop(f, Config{})
	if d == nil {
		t.Fatalf("65a6d56e is not a drop: %s", why)
	}
	if d.phase != "foldgate" {
		t.Errorf("drop of 65a6d56e resumes from %q, want foldgate (where its replay began)", d.phase)
	}

	// Not a replay: its own work is the whole flow.
	f.replayKnown, f.replayFrom = false, ""
	if got, first := resumeFrom(f), runnablePhases(f.phases)[0].ID; got != first {
		t.Errorf("a run through every phase that is no replay resumes from %q, want %s", got, first)
	}
	// A forward phase of its own work that ended failed, though the flow went
	// on, is where it goes again from.
	f.replayKnown, f.replayFrom = true, "foldgate"
	f.rec["publish"].failed = true
	if got := resumeFrom(f); got != "publish" {
		t.Errorf("with publish ended failed the resume is from %q, want publish", got)
	}
	// No flow on the record: the orchestrator decides, as before.
	if got := resumeFrom(&runFacts{rec: map[string]*phaseRecord{}}); got != "" {
		t.Errorf("with no flow the resume is from %q, want \"\"", got)
	}
}

// A drop already recorded with no phase - the chain of 65a6d56e, recorded
// before the watchdog could name one - is resumed from the phase the record
// names, read again off the dropped run when its resume is queued.
func TestDeployedCycleOfADropWithNoPhaseResumesFromANamedPhase(t *testing.T) {
	const run = "65a6d56e-5ed9-4b35-8960-dc2d5dcc10ca"
	if err := openDB(t.TempDir() + "/ammit.db"); err != nil {
		t.Fatal(err)
	}
	loadRunRecord(t, run)
	if _, err := db.Exec(`INSERT INTO watchdog_drops (run, root, name, mode, phase, cause, verdict, evidence, at)
	                      VALUES (?,?,?,?,?,?,?,?,?)`, run, run, "APF-3296", "functional-tester", "",
		"verdict:unknown", "UNKNOWN", "[]", 1791988571.1353147); err != nil {
		t.Fatal(err)
	}
	d, ok := dropOfRun(run)
	if !ok || d.phase != "" {
		t.Fatalf("the recorded drop is %+v (%v), want one with no phase", d, ok)
	}
	// Cycle 1 as it stood: #699 merged at 14:48:07Z, deployed at 14:54:40Z.
	res, err := db.Exec(`INSERT INTO watchdog_cycles (root, run, cycle, state, branch, merged_at,
	                       started, entered, updated) VALUES (?,?,?,?,?,?,?,?,?)`,
		run, run, 1, wdDeploying, "watchdog/65a6d56e-1", 1791989287.0,
		1791988571.1387255, 1791989311.5, 1791989311.5)
	if err != nil {
		t.Fatal(err)
	}
	cycle, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO deploys (at, status) VALUES (?, 'success')`,
		1791989680.3011382); err != nil {
		t.Fatal(err)
	}
	stepCycle(wdCycles(`id=?`, cycle)[0], Config{}, 3)
	c := wdCycles(`id=?`, cycle)[0]
	if c.state != wdResumed {
		t.Fatalf("cycle 1 is %s after its deploy, want %s", c.state, wdResumed)
	}
	var payload string
	if err := db.QueryRow(`SELECT payload FROM queue WHERE id=?`, c.resumeQueue.Int64).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(payload), &body); err != nil {
		t.Fatal(err)
	}
	if body["from_phase"] != "foldgate" || body["rerun"] != "resume" || body["source_run"] != run {
		t.Errorf("the resume queued is %s, want rerun=resume from_phase=foldgate source_run=%s",
			payload, run)
	}
	if got := startAnswer("APF-3296", 1791989693.6470208); got != "" {
		t.Errorf("startAnswer with nothing recorded = %q, want \"\"", got)
	}
	judge("queue", "", "APF-3296", "queue.parallel", 1, 1, "started",
		"run 65a6d56e got through all 42 phases, so there is nothing to resume")
	if got := startAnswer("APF-3296", wdNow()-5); !strings.Contains(got, "nothing to resume") {
		t.Errorf("startAnswer = %q, want the orchestrator's refusal", got)
	}
}

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

// A chain of a host run that was already moving when classifyDrop learned to
// refuse host runs (#91) ends at its next step rather than cycling on. Read off
// the record of APF-2327's host run 28a6a227 (run_start tags.where = local):
// cycle 2 was queued at 00:35:00, after #91 at 00:34:20, because stepCycle
// failed cycle 1 into a new cycle without asking again.
func TestMovingCycleOfAHostRunIsLeftNotCycledOn(t *testing.T) {
	const hostDropped = "28a6a227-392b-4e19-a779-f9c2a6ebf85a"
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
	mustExec(`INSERT INTO runs (run, name, started, finished, verdict) VALUES (?,?,?,?,?)`,
		hostDropped, "APF-2327", 1791761558.293004, 1791765040.9700053, "BLOCKED")
	mustExec(`INSERT INTO events (at, kind, run, payload) VALUES (?, 'run_start', ?, ?)`,
		1791761558.293004, hostDropped,
		`{"kind":"run_start","name":"APF-2327","run":"`+hostDropped+`","tags":{"mode":"data-tester","where":"local"}}`)
	mustExec(`INSERT INTO watchdog_drops (run, root, name, mode, phase, cause, verdict, evidence, at)
	          VALUES (?,?,?,?,?,?,?,?,?)`, hostDropped, hostDropped, "APF-2327",
		"data-tester", "", "limit:loops.laps_standfix", "BLOCKED", "[]", 1791765057.9735928)
	fix := mustExec(`INSERT INTO queue (name, payload, requested) VALUES (?,?,?)`,
		"WD-28a6a227-2", "{}", 1791765300.4216988)
	cycle := mustExec(`INSERT INTO watchdog_cycles (root, run, cycle, state, branch, fix_queue,
	                     started, entered, updated) VALUES (?,?,?,?,?,?,?,?,?)`,
		hostDropped, hostDropped, 2, wdFixing, "watchdog/28a6a227-2", fix,
		1791765300.4216988, 1791765300.4216988, 1791765300.4216988)

	stepCycle(wdCycles(`id=?`, cycle)[0], Config{}, 3)

	if c := wdCycles(`id=?`, cycle)[0]; c.state != wdLeft {
		t.Fatalf("cycle 2 of the host run %s is %s, want %s", hostDropped, c.state, wdLeft)
	}
	var queued string
	if err := db.QueryRow(`SELECT state FROM queue WHERE id=?`, fix).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != "done" {
		t.Errorf("the fix run still waiting to start is %s, want withdrawn (done)", queued)
	}
	if n := len(wdCycles(`root=?`, hostDropped)); n != 1 {
		t.Errorf("the host run's chain has %d cycles after the step, want 1: no next cycle", n)
	}

	// A stack run's moving cycle is untouched by the rule: still fixing.
	mustExec(`INSERT INTO watchdog_drops (run, root, name, mode, phase, cause, verdict, evidence, at)
	          VALUES (?,?,?,?,?,?,?,?,?)`, wdTestDropped, wdTestDropped, "APF-3296",
		"functional-tester", "bugsrules", "limit:loops.laps_bugsfix", "BLOCKED", "[]", 1791687016.2542412)
	stack := mustExec(`INSERT INTO watchdog_cycles (root, run, cycle, state, branch, fix_queue,
	                     started, entered, updated) VALUES (?,?,?,?,?,?,?,?,?)`,
		wdTestDropped, wdTestDropped, 1, wdFixing, "watchdog/db62b413-1", nil,
		wdNow(), wdNow(), wdNow())
	stepCycle(wdCycles(`id=?`, stack)[0], Config{}, 3)
	if c := wdCycles(`id=?`, stack)[0]; c.state != wdFixing {
		t.Errorf("a stack run's fixing cycle went to %s, want it still fixing", c.state)
	}
}

// A drop whose ticket was started over is answered by the new run, and the
// moving chain ends rather than resuming the old run on top of it. Read off
// APF-3301: 03405ee1 stopped by a .cancel no judgement wrote, its drop recorded
// at 1792621022.59 with cycle 1 fixing, and a fresh run b6c05e40 - no replay
// event - started at 1792621025.61. A replay of the dropped run is still the
// resume the cycle follows, a replay of another run is not a restart
// (TestFixingCycleFollowsAResumeStartedOutsideIt), and a ticket nobody
// restarted keeps its cycle.
func TestMovingCycleOfASupersededRunIsLeftNotResumed(t *testing.T) {
	const (
		dropped = "03405ee1-840f-498b-941b-c681819bc1f7"
		fresh   = "b6c05e40-0a55-471d-a7bf-6164d73c7e1d"
		at      = 1792621022.5935090
	)
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
	drop := func(run, name string, started float64) int64 {
		mustExec(`INSERT INTO runs (run, name, started, finished, verdict) VALUES (?,?,?,?,?)`,
			run, name, started, started+440, "BLOCKED")
		mustExec(`INSERT INTO watchdog_drops (run, root, name, mode, phase, cause, verdict, evidence, at)
		          VALUES (?,?,?,?,?,?,?,?,?)`, run, run, name, "functional-tester",
			"envplanrules", "phase_failed", "BLOCKED", "[]", started+452)
		fix := mustExec(`INSERT INTO queue (name, payload, requested) VALUES (?,?,?)`,
			"WD-"+run[:8]+"-1", "{}", started+452)
		return mustExec(`INSERT INTO watchdog_cycles (root, run, cycle, state, branch, fix_queue,
		                   started, entered, updated) VALUES (?,?,?,?,?,?,?,?,?)`,
			run, run, 1, wdFixing, "watchdog/"+run[:8]+"-1", fix,
			started+452, started+452, started+452)
	}

	cycle := drop(dropped, "APF-3301", at-451.7)
	mustExec(`INSERT INTO runs (run, name, started) VALUES (?,?,?)`, fresh, "APF-3301",
		1792621025.614544)
	// Between its run_start and its first phase a run may still be about to
	// say it is a replay: not yet read as the ticket started over.
	stepCycle(wdCycles(`id=?`, cycle)[0], Config{}, 3)
	if c := wdCycles(`id=?`, cycle)[0]; c.state != wdFixing {
		t.Fatalf("cycle 1 of %s went to %s before %s began a phase, want it still fixing",
			dropped, c.state, fresh)
	}
	mustExec(`INSERT INTO events (at, kind, run, phase, payload) VALUES (?, 'phase_start', ?, ?, ?)`,
		1792621031.0, fresh, "branch", `{"kind":"phase_start","run":"`+fresh+`","phase":"branch"}`)
	stepCycle(wdCycles(`id=?`, cycle)[0], Config{}, 3)
	c := wdCycles(`id=?`, cycle)[0]
	if c.state != wdLeft {
		t.Fatalf("cycle 1 of %s is %s after %s started over, want %s", dropped, c.state, fresh, wdLeft)
	}
	if !strings.Contains(c.why, fresh) {
		t.Errorf("the cycle's why does not name the run that superseded it: %q", c.why)
	}
	var queued string
	if err := db.QueryRow(`SELECT state FROM queue WHERE id=?`, c.fixQueue.Int64).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != "done" {
		t.Errorf("the fix run still waiting to start is %s, want withdrawn (done)", queued)
	}

	// A replay of the dropped run is the resume, not a supersede.
	const replayed, replay = "11111111-0000-4000-8000-000000000001", "11111111-0000-4000-8000-000000000002"
	followed := drop(replayed, "APF-0001", at)
	mustExec(`INSERT INTO runs (run, name, started) VALUES (?,?,?)`, replay, "APF-0001", at+900)
	mustExec(`INSERT INTO events (at, kind, run, payload) VALUES (?, 'replay', ?, ?)`, at+900, replay,
		`{"kind":"replay","run":"`+replay+`","from_phase":"envplanrules","source_run":"`+replayed+`"}`)
	stepCycle(wdCycles(`id=?`, followed)[0], Config{}, 3)
	if c := wdCycles(`id=?`, followed)[0]; c.state != wdResumed || c.resumedRun != replay {
		t.Errorf("a replayed drop's cycle is %s following %q, want %s following %s",
			c.state, c.resumedRun, wdResumed, replay)
	}

	// A ticket nobody started over keeps its cycle.
	alone := drop("22222222-0000-4000-8000-000000000001", "APF-0002", at)
	stepCycle(wdCycles(`id=?`, alone)[0], Config{}, 3)
	if c := wdCycles(`id=?`, alone)[0]; c.state != wdFixing {
		t.Errorf("a cycle with no later run of its ticket went to %s, want it still fixing", c.state)
	}
}
