package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// The watchdog that does not let a run stay dropped.
//
// Every rule in judge.go ends a run or warns about one, and then nothing: a
// run closed as stillborn, a gate still red after its repair laps, a run
// killed by a limit or one that sat waiting on a person - each waited for
// somebody to read the record, fix the cause, merge, deploy and start the
// ticket again from where it stopped. On 9 October APF-2327 dropped eleven
// times in one day, funcreqrules twice in a row on the same contradiction, and
// every restart was a person doing the same five things by hand.
//
// So a drop is a fact this service records and then answers, in a loop:
//
//	detected  the run ended or stalled not green; the cause and the evidence
//	          lines are written down as a `watchdog` event and a judgement
//	fixing    a fix run is queued (watchdog.fix_mode) with the evidence; its
//	          job is a PR on branch watchdog/<run8>-<cycle>
//	ci        the PR exists; its checks are read until they are green
//	merging   checks green: commands.watchdog_merge, until GitHub says MERGED
//	deploying merged: a deploy that reported success after the merge
//	resumed   deployed: the ticket is queued with rerun=resume from the phase
//	          that dropped, behind any live run (queue.parallel)
//	done      the resumed run ended green
//
// A resumed run that drops again starts the next cycle of the same chain, and
// a cycle that fails on the way (no PR, red CI, no deploy) is a cycle spent.
// loops.laps_watchdog is how many cycles one chain may take; the one past it
// is not taken, it is announced (actions notify) with the whole history.
//
// Every decision reads evidence - the run's row, its events, its judgements,
// the queue, the deploy record, and what GitHub says about a PR - and never
// what an agent wrote. The fix run's own words are not read at all: a PR on
// the branch exists or it does not, and its checks are green or they are not.
//
// State lives in the database, so a restart of this service picks every cycle
// up where it was and announces nothing twice.

const watchdogSchema = `
CREATE TABLE IF NOT EXISTS watchdog_drops (
    run      TEXT PRIMARY KEY,   -- the run that dropped
    root     TEXT NOT NULL,      -- the first drop of its chain
    name     TEXT,
    mode     TEXT,
    phase    TEXT,               -- where a resume starts; '' lets the orchestrator decide
    cause    TEXT NOT NULL,      -- an id: stillborn, gate_red_laps, limit:<rule>, waiting, ...
    verdict  TEXT,
    evidence TEXT,               -- JSON list of lines, read from the record
    at       REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS watchdog_cycles (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    root         TEXT NOT NULL,
    run          TEXT NOT NULL,  -- the drop this cycle answers
    cycle        INTEGER NOT NULL,
    state        TEXT NOT NULL,
    branch       TEXT NOT NULL,  -- the head branch the fix's PR must be on
    fix_queue    INTEGER,
    prs          TEXT,           -- JSON list of PR urls on that branch
    merged_at    REAL,
    deployed_at  REAL,
    resume_queue INTEGER,
    resumed_run  TEXT,
    why          TEXT,
    started      REAL NOT NULL,
    entered      REAL,           -- when it came into its present state
    updated      REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS watchdog_cycles_root ON watchdog_cycles (root, cycle);
CREATE TABLE IF NOT EXISTS watchdog_meta (name TEXT PRIMARY KEY, value REAL);`

// The states a cycle can be in. The first five move; the rest are where a
// cycle ends.
const (
	wdFixing    = "fixing"
	wdCI        = "ci"
	wdMerging   = "merging"
	wdDeploying = "deploying"
	wdResumed   = "resumed"
	wdDone      = "done"      // the resumed run ended green
	wdAgain     = "dropped"   // the resumed run dropped: the next cycle answers it
	wdFailed    = "failed"    // the cycle did not get to a resume
	wdLeft      = "left"      // the resumed run ended in a way that is not a drop
	wdExhausted = "exhausted" // loops.laps_watchdog reached: announced, not taken
)

var wdMoving = []string{wdFixing, wdCI, wdMerging, wdDeploying, wdResumed}

// Verdicts that are not a drop. SKIPPED is a run that found its ticket
// already being run by another: the ticket is not dropped, it is busy.
var wdNotDropped = map[string]bool{
	"GREEN": true, "OK": true, "PASS": true, "PASSED": true, "SKIPPED": true,
}

// wdGreen is what a resumed run must end as for its cycle to be done.
var wdGreen = map[string]bool{"GREEN": true, "OK": true, "PASS": true, "PASSED": true}

// What counts as a run getting on with it. A heartbeat is the process
// looping, a note is a line said on a timer ("waiting for approval (40m)"),
// a log line can be a polling loop: none of those is work, and a run whose
// newest event of these kinds is old is waiting, whatever else it says.
var wdProgress = []string{"phase_start", "phase_end", "gate", "turn", "call",
	"item_start", "item_end", "session_start", "session_end", "request_end",
	"spend", "suite", "heal_lap", "rule_verdict"}

func wdNow() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// watchdogCap is loops.laps_watchdog: cycles per chain. Absent or zero, the
// watchdog is off, like every limit here whose zero is a decision.
func watchdogCap(conf Config) (float64, bool) {
	return conf.num("loops", lapsPrefix+"watchdog")
}

// ---- detection -----------------------------------------------------------

// flowPhase is one phase of the flow a run executed, as its `flow` event said.
type flowPhase struct {
	ID      string   `json:"id"`
	Agents  []string `json:"agents"`
	OnlyIf  string   `json:"only_if"`
	LoopsTo string   `json:"loops_to"`
}

type phaseRecord struct {
	starts, ends int
	verdicts     map[string]string // branch -> last gate verdict
	failed       bool              // a phase_end said failed, or carried an error
	lastText     string
	lastError    string
}

// verdict is the phase's gate as a whole: red if any branch is red, blocked
// if any is blocked, green if every judged branch is green, "" when no gate
// judged it.
func (p *phaseRecord) verdict() string {
	if p == nil || len(p.verdicts) == 0 {
		return ""
	}
	out := "green"
	for _, v := range p.verdicts {
		switch v {
		case "red":
			return "red"
		case "blocked":
			out = "blocked"
		}
	}
	return out
}

func (p *phaseRecord) succeeded() bool {
	if p == nil || p.starts == 0 {
		return false
	}
	if v := p.verdict(); v != "" {
		return v == "green"
	}
	return p.ends >= p.starts && !p.failed
}

// runFacts is everything the record holds that a drop is judged on.
type runFacts struct {
	run, name, mode, verdict, summary string
	started, finished                 float64
	open                              bool
	heartbeats                        int
	runEnd                            bool
	phases                            []flowPhase
	rec                               map[string]*phaseRecord
	replayFrom                        string
	replayKnown                       bool
	judgements                        []judgementFact
	// When later runs of the same ticket started. Which of them count is a
	// question of when the drop is read: seenAt.
	newer []float64
	// When the drop is read. Live that is now; over history it is a tick after
	// the run finished, so a person's rerun an hour later does not hide a drop
	// the watchdog would have answered at the time. Zero is now.
	seenAt float64
	// Whether the pipeline ever opened this run. A row with no run_start was
	// made by a stray event landing after its run had gone (store() gives any
	// event a row), and its name is its id: nothing there ever ran.
	runStart bool
}

type judgementFact struct {
	scope, rule, action, subject string
	threshold, observed          float64
}

func readRunFacts(run string) (*runFacts, error) {
	f := &runFacts{run: run, rec: map[string]*phaseRecord{}}
	mu.Lock()
	defer mu.Unlock()
	var name, verdict, summary sql.NullString
	var started, finished sql.NullFloat64
	err := db.QueryRow(`SELECT name, started, finished, verdict, summary FROM runs WHERE run=?`,
		run).Scan(&name, &started, &finished, &verdict, &summary)
	if err != nil {
		return nil, err
	}
	f.name, f.verdict, f.summary = name.String, strings.ToUpper(verdict.String), summary.String
	f.started, f.finished, f.open = started.Float64, finished.Float64, !finished.Valid
	f.newer = laterStarts(f.name, run, f.started)
	var starts int
	db.QueryRow(`SELECT count(*) FROM events WHERE run=? AND kind='run_start'`, run).Scan(&starts)
	f.runStart = starts > 0
	db.QueryRow(`SELECT coalesce(json_extract(payload,'$.tags.mode'),'') FROM events
	             WHERE run=? AND kind IN ('run_start','note')
	               AND ifnull(json_extract(payload,'$.tags.mode'),'') <> ''
	             ORDER BY id LIMIT 1`, run).Scan(&f.mode)
	var flowMode, phases string
	if db.QueryRow(`SELECT coalesce(mode,''), coalesce(phases,'') FROM flows WHERE run=?`,
		run).Scan(&flowMode, &phases) == nil {
		if flowMode != "" {
			f.mode = flowMode
		}
		_ = json.Unmarshal([]byte(phases), &f.phases)
	}
	var from sql.NullString
	if db.QueryRow(`SELECT json_extract(payload,'$.from_phase') FROM events
	                WHERE run=? AND kind='replay' ORDER BY id DESC LIMIT 1`, run).Scan(&from) == nil {
		f.replayKnown, f.replayFrom = true, from.String
	}
	db.QueryRow(`SELECT count(*) FROM events WHERE run=? AND kind='heartbeat'`, run).Scan(&f.heartbeats)
	var ends int
	db.QueryRow(`SELECT count(*) FROM events WHERE run=? AND kind='run_end'`, run).Scan(&ends)
	f.runEnd = ends > 0
	rows, err := db.Query(`SELECT coalesce(phase,''), coalesce(branch,''), kind,
	                         coalesce(json_extract(payload,'$.verdict'),''),
	                         coalesce(json_extract(payload,'$.failed'),0),
	                         coalesce(json_extract(payload,'$.ok'),1),
	                         coalesce(json_extract(payload,'$.error'),''),
	                         coalesce(substr(json_extract(payload,'$.text'),1,600),'')
	                       FROM events WHERE run=? AND kind IN ('phase_start','phase_end','gate')
	                       ORDER BY at, id`, run)
	if err == nil {
		for rows.Next() {
			var phase, branch, kind, verdict, errText, text string
			var failed, ok any
			if rows.Scan(&phase, &branch, &kind, &verdict, &failed, &ok, &errText, &text) != nil || phase == "" {
				continue
			}
			p := f.rec[phase]
			if p == nil {
				p = &phaseRecord{verdicts: map[string]string{}}
				f.rec[phase] = p
			}
			switch kind {
			case "phase_start":
				p.starts++
			case "phase_end":
				p.ends++
				p.failed = truthy(failed) || falsy(ok) || errText != ""
				p.lastError, p.lastText = errText, text
			case "gate":
				p.verdicts[branch] = strings.ToLower(verdict)
			}
		}
		rows.Close()
	}
	f.judgements = judgementsOf(run)
	return f, nil
}

// laterStarts is when every later run of the ticket started. Under mu.
func laterStarts(name, run string, after float64) []float64 {
	rows, err := db.Query(`SELECT started FROM runs WHERE name=? AND run<>? AND started > ?`,
		name, run, after)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []float64
	for rows.Next() {
		var at float64
		if rows.Scan(&at) == nil {
			out = append(out, at)
		}
	}
	return out
}

// judgementsOf is every judgement written about a run. Under mu.
func judgementsOf(run string) []judgementFact {
	rows, err := db.Query(`SELECT scope, rule, action, coalesce(subject,''),
	                         coalesce(threshold,0), coalesce(observed,0)
	                       FROM judgements WHERE run=? ORDER BY id`, run)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []judgementFact
	for rows.Next() {
		var j judgementFact
		if rows.Scan(&j.scope, &j.rule, &j.action, &j.subject, &j.threshold, &j.observed) == nil {
			out = append(out, j)
		}
	}
	return out
}

// truthy and falsy read a JSON boolean or number the way the runner sends it.
func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case int64:
		return x != 0
	case float64:
		return x != 0
	}
	return false
}

func falsy(v any) bool {
	switch x := v.(type) {
	case bool:
		return !x
	case int64:
		return x == 0
	case float64:
		return x == 0
	}
	return false
}

// runnablePhases are the phases the runner executes: the ones with an agent.
func runnablePhases(phases []flowPhase) []flowPhase {
	var out []flowPhase
	for _, p := range phases {
		if len(p.Agents) > 0 {
			out = append(out, p)
		}
	}
	return out
}

// forwardPhase says whether the flow reaches phase i by getting on with it,
// rather than by failing: a repair runs only on red (or on requested changes)
// and loops back to an earlier gate. The same reading as the orchestrator's
// resume.go, so the phase named here is one its resume accepts.
func forwardPhase(phases []flowPhase, i int) bool {
	for _, cond := range strings.Fields(strings.ToLower(phases[i].OnlyIf)) {
		if cond == "red" || cond == "changes" {
			return false
		}
	}
	for j, p := range phases {
		if p.ID == phases[i].LoopsTo && j < i {
			return false
		}
	}
	return true
}

// resumeFrom is the phase a resume of this run starts at: the first forward
// phase that did not get through. Finished work is never redone - a phase
// before the frontier that started and ended is done, and a phase the run
// inherited from the run it replayed is done too. "" when the record cannot
// say (no flow event): the orchestrator's own resume decides then.
func resumeFrom(f *runFacts) string {
	at := frontierFrom(f)
	// A fan-out carries a branch whose repairs ran out and goes on without
	// it, so the frontier can be the last phase of a run whose real drop is a
	// gate halfway up: "the scenarios that ran passed, but a repair loop ran
	// out of attempts in claim-2". The earliest forward gate still red or
	// blocked on a fan-out branch is where the work was dropped, when it comes
	// before the frontier's answer. Only a branch: a run-wide gate the flow
	// went past red was answered by its own repair (env blocked, envd green)
	// and is the orchestrator's frontier to judge, not this.
	phases := runnablePhases(f.phases)
	for i, p := range phases {
		if p.ID == at {
			break
		}
		if !forwardPhase(phases, i) {
			continue
		}
		if r := f.rec[p.ID]; r != nil {
			for branch, v := range r.verdicts {
				if branch != "" && (v == "red" || v == "blocked") {
					return p.ID
				}
			}
		}
	}
	return at
}

// frontierFrom is the orchestrator's own reading of where a run stopped
// (resume.go resumePoint), over this service's record.
func frontierFrom(f *runFacts) string {
	phases := runnablePhases(f.phases)
	if len(phases) == 0 {
		return ""
	}
	frontier := -1
	for i, p := range phases {
		if r := f.rec[p.ID]; r != nil && r.starts > 0 && forwardPhase(phases, i) {
			frontier = i
		}
	}
	if frontier < 0 {
		// Nothing started: a stillborn run, or one that waited before its
		// first phase. A replay starts again where it was asked to.
		if f.replayKnown && f.replayFrom != "" {
			return f.replayFrom
		}
		return phases[0].ID
	}
	inherited := frontier
	if f.replayKnown && f.replayFrom != "" {
		inherited = 0
		for i, p := range phases {
			if p.ID == f.replayFrom {
				inherited = i
				break
			}
		}
	}
	for i := 0; i < frontier; i++ {
		p := phases[i]
		r := f.rec[p.ID]
		if r == nil || r.starts == 0 {
			if p.OnlyIf == "" && i >= inherited {
				return p.ID
			}
			continue
		}
		if r.ends < r.starts {
			// A repair cut off mid-lap resumes at the gate it answers: the
			// gate decides whether another lap is wanted, and starting the
			// repair cold would repair what nobody has judged red yet.
			if !forwardPhase(phases, i) && p.LoopsTo != "" {
				return p.LoopsTo
			}
			return p.ID
		}
	}
	if !f.rec[phases[frontier].ID].succeeded() {
		return phases[frontier].ID
	}
	for i := frontier + 1; i < len(phases); i++ {
		if forwardPhase(phases, i) {
			return phases[i].ID
		}
	}
	return ""
}

// repairOf is the repair phase that loops back to a gate, "" if none does.
func repairOf(phases []flowPhase, gate string) string {
	for i, p := range phases {
		if p.LoopsTo == gate && !forwardPhase(phases, i) {
			return p.ID
		}
	}
	return ""
}

// dropFact is one drop, as recorded.
type dropFact struct {
	run, name, mode, phase, cause, verdict string
	evidence                               []string
}

// classifyDrop decides whether a finished run dropped, and why. The second value
// is why it is not a drop, when it is not one. Read off the record only: the
// verdict, the judgements this service and the runner wrote, the gates, the
// heartbeats. The summary is prose and is carried as evidence for the fix,
// never weighed.
func classifyDrop(f *runFacts, conf Config) (*dropFact, string) {
	if f.open {
		return nil, "still open"
	}
	if wdNotDropped[f.verdict] {
		return nil, "verdict " + f.verdict
	}
	if fix := conf.str("watchdog", "fix_mode", "watchdogfix"); f.mode == fix {
		return nil, "a watchdog fix run"
	}
	for _, j := range f.judgements {
		if j.scope == "hand" && stops(j.action) {
			return nil, "stopped by a person (" + j.rule + ")"
		}
	}
	if !f.runStart && f.name == f.run {
		return nil, "no run_start: a row made by a stray event, not a run"
	}
	for _, at := range f.newer {
		if f.seenAt == 0 || at <= f.seenAt {
			return nil, "superseded by a later run of " + f.name
		}
	}
	d := &dropFact{run: f.run, name: f.name, mode: f.mode, verdict: f.verdict,
		phase: resumeFrom(f)}
	d.evidence = append(d.evidence, fmt.Sprintf("run %s (%s, mode %s) ended %s: %s",
		f.run, f.name, orDash(f.mode), orDash(f.verdict), f.summary))
	if d.phase != "" {
		d.evidence = append(d.evidence, "dropped at phase "+d.phase)
	}
	d.cause = causeOf(f, conf, d)
	return d, ""
}

// causeOf names the cause as an id and adds the lines that show it.
func causeOf(f *runFacts, conf Config, d *dropFact) string {
	// A limit that ended the run, by this service or by the runner.
	for _, j := range f.judgements {
		if j.scope == "hand" {
			continue
		}
		if stops(j.action) || endsRun(conf, j.action) || j.action == "stop_branch" ||
			j.action == "stopped" || j.action == "gave_up" {
			d.evidence = append(d.evidence, fmt.Sprintf("judgement %s on %s: %.0f against %.0f -> %s",
				j.rule, orDash(j.subject), j.observed, j.threshold, j.action))
			return "limit:" + j.rule
		}
	}
	if f.heartbeats == 0 && len(f.rec) == 0 {
		d.evidence = append(d.evidence, "no heartbeat and no phase ever started")
		return "stillborn"
	}
	if !f.runEnd {
		// Nobody in the pipeline wrote the ending: this service closed the
		// row because the process behind it was gone.
		d.evidence = append(d.evidence, fmt.Sprintf(
			"no run_end from the pipeline; %d heartbeat(s); closed by ammit as %s",
			f.heartbeats, f.verdict))
		if f.verdict == "ABANDONED" {
			return "abandoned"
		}
		return "crash"
	}
	if r := f.rec[d.phase]; r != nil {
		switch r.verdict() {
		case "red", "blocked":
			for branch, v := range r.verdicts {
				if v == "red" || v == "blocked" {
					d.evidence = append(d.evidence, fmt.Sprintf("gate %s %s",
						phaseKey(d.phase, branch), v))
				}
			}
			if r.lastText != "" {
				d.evidence = append(d.evidence, "last words of "+d.phase+": "+r.lastText)
			}
			if r.verdict() == "blocked" {
				return "gate_blocked"
			}
			if repair := repairOf(f.phases, d.phase); repair != "" {
				laps := 0
				if rr := f.rec[repair]; rr != nil {
					laps = rr.starts
				}
				d.evidence = append(d.evidence, fmt.Sprintf("repair %s started %d time(s)",
					repair, laps))
				if limit, ok := conf.num("loops", lapsPrefix+repair); ok && float64(laps) >= limit {
					d.evidence[len(d.evidence)-1] += fmt.Sprintf(
						" against loops.%s%s = %.0f: exhausted", lapsPrefix, repair, limit)
					return "gate_red_laps"
				}
			}
			return "gate_red"
		}
		if r.failed || r.ends < r.starts {
			if r.lastError != "" {
				d.evidence = append(d.evidence, "phase "+d.phase+" error: "+r.lastError)
			}
			if r.lastText != "" {
				d.evidence = append(d.evidence, "last words of "+d.phase+": "+r.lastText)
			}
			return "phase_failed"
		}
	}
	return "verdict:" + strings.ToLower(orDash(f.verdict))
}

// waitingOn says whether an open run has made no progress for longer than it
// may wait, and how long it has not. The limit is limits.wait_<phase> for the
// phase it is in, limits.wait otherwise; neither set, nothing is judged.
// Measured awake, like every age here.
func waitingOn(run string, conf Config) (quiet, limit float64, rule, phase string, waiting bool) {
	mu.Lock()
	db.QueryRow(`SELECT coalesce(phase,'') FROM events
	             WHERE run=? AND kind='phase_start' AND ifnull(phase,'') <> ''
	               AND coalesce(phase,'') || '@' || coalesce(branch,'') NOT IN (
	                   SELECT coalesce(phase,'') || '@' || coalesce(branch,'')
	                   FROM events WHERE run=? AND kind='phase_end')
	             ORDER BY at DESC LIMIT 1`, run, run).Scan(&phase)
	var last sql.NullFloat64
	db.QueryRow(`SELECT max(at) FROM events WHERE run=? AND kind IN ('`+
		strings.Join(wdProgress, "','")+`')`, run).Scan(&last)
	var started float64
	db.QueryRow(`SELECT coalesce(started,0) FROM runs WHERE run=?`, run).Scan(&started)
	mu.Unlock()
	rule = "limits.wait"
	limit, ok := conf.num("limits", "wait")
	if phase != "" {
		if own, has := conf.num("limits", "wait_"+phase); has {
			limit, ok, rule = own, true, "limits.wait_"+phase
		}
	}
	if !ok || limit <= 0 {
		return 0, 0, "", phase, false
	}
	since := started
	if last.Valid && last.Float64 > since {
		since = last.Float64
	}
	quiet = wdNow() - since
	if up := wdNow() - _upSince; up < quiet {
		quiet = up
	}
	return quiet, limit, rule, phase, quiet > limit
}

// ---- the record ----------------------------------------------------------

func wdMeta(name string) (float64, bool) {
	mu.Lock()
	defer mu.Unlock()
	var v float64
	if db.QueryRow(`SELECT value FROM watchdog_meta WHERE name=?`, name).Scan(&v) != nil {
		return 0, false
	}
	return v, true
}

func wdSetMeta(name string, v float64) {
	mu.Lock()
	defer mu.Unlock()
	db.Exec(`INSERT OR IGNORE INTO watchdog_meta (name, value) VALUES (?,?)`, name, v)
}

// wdSay writes the watchdog's own event, where the dashboard and /search read
// every other one.
func wdSay(run, phase, state, cause string, cycle int, limit float64, prs []string,
	text string) {
	e := event{"kind": "watchdog", "run": run, "phase": phase, "state": state,
		"cause": cause, "cycle": float64(cycle), "cap": limit, "text": text}
	if len(prs) > 0 {
		e["pr"] = strings.Join(prs, " ")
	}
	store(e)
	log.Printf("ammit: watchdog %s %s cycle %d/%.0f %s: %s", run, state, cycle, limit,
		cause, firstLine(text))
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// recordDrop writes the drop down once. false when it was already there: a
// restart, or a second tick, must not answer one drop twice.
func recordDrop(d *dropFact, root string) bool {
	ev, _ := json.Marshal(d.evidence)
	mu.Lock()
	res, err := db.Exec(`INSERT OR IGNORE INTO watchdog_drops
	         (run, root, name, mode, phase, cause, verdict, evidence, at)
	         VALUES (?,?,?,?,?,?,?,?,?)`,
		d.run, root, d.name, d.mode, d.phase, d.cause, d.verdict, string(ev), wdNow())
	mu.Unlock()
	if err != nil {
		log.Printf("ammit: watchdog could not record the drop of %s: %v", d.run, err)
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// rootOf is the chain a drop belongs to: the root of the cycle whose resume
// became this run, or the run itself. The queue row is the link - a resume is
// queued, the queue row is tied to the run it turned into on run_start - so
// the chain is read from what happened, not from a name.
func rootOf(run string) (string, int64) {
	mu.Lock()
	defer mu.Unlock()
	var root string
	var id int64
	err := db.QueryRow(`SELECT c.root, c.id FROM watchdog_cycles c
	                    JOIN queue q ON q.id = c.resume_queue
	                    WHERE q.run = ? ORDER BY c.id DESC LIMIT 1`, run).Scan(&root, &id)
	if err != nil {
		return run, 0
	}
	return root, id
}

type wdCycle struct {
	id                    int64
	root, run, state, why string
	branch, resumedRun    string
	cycle                 int
	fixQueue, resumeQueue sql.NullInt64
	prs                   []string
	mergedAt, deployedAt  sql.NullFloat64
	started, entered      float64
}

func wdCycles(where string, args ...any) []wdCycle {
	mu.Lock()
	defer mu.Unlock()
	rows, err := db.Query(`SELECT id, root, run, cycle, state, branch, fix_queue,
	                         coalesce(prs,'[]'), merged_at, deployed_at, resume_queue,
	                         coalesce(resumed_run,''), coalesce(why,''), started,
	                         coalesce(entered, started)
	                       FROM watchdog_cycles WHERE `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []wdCycle
	for rows.Next() {
		var c wdCycle
		var prs string
		if rows.Scan(&c.id, &c.root, &c.run, &c.cycle, &c.state, &c.branch, &c.fixQueue,
			&prs, &c.mergedAt, &c.deployedAt, &c.resumeQueue, &c.resumedRun, &c.why,
			&c.started, &c.entered) == nil {
			_ = json.Unmarshal([]byte(prs), &c.prs)
			out = append(out, c)
		}
	}
	return out
}

func wdUpdate(id int64, set string, args ...any) {
	mu.Lock()
	defer mu.Unlock()
	args = append([]any{wdNow()}, args...)
	args = append(args, id)
	db.Exec(`UPDATE watchdog_cycles SET updated=?, `+set+` WHERE id=?`, args...)
}

// wdMove puts a cycle into a new state, with whatever else changed on the way.
func wdMove(id int64, state, set string, args ...any) {
	if set != "" {
		set = ", " + set
	}
	wdUpdate(id, "state=?, entered=?"+set, append([]any{state, wdNow()}, args...)...)
}

// spentCycles is how many cycles a chain has taken: every one that was
// started, whatever became of it. The announcement of exhaustion is a row too
// and is not a cycle.
func spentCycles(root string) int {
	mu.Lock()
	defer mu.Unlock()
	var n int
	db.QueryRow(`SELECT count(*) FROM watchdog_cycles WHERE root=? AND state<>?`,
		root, wdExhausted).Scan(&n)
	return n
}

func dropOfRun(run string) (*dropFact, bool) {
	mu.Lock()
	defer mu.Unlock()
	d := &dropFact{run: run}
	var ev string
	err := db.QueryRow(`SELECT coalesce(name,''), coalesce(mode,''), coalesce(phase,''),
	                      cause, coalesce(verdict,''), coalesce(evidence,'[]')
	                    FROM watchdog_drops WHERE run=?`, run).
		Scan(&d.name, &d.mode, &d.phase, &d.cause, &d.verdict, &ev)
	if err != nil {
		return nil, false
	}
	_ = json.Unmarshal([]byte(ev), &d.evidence)
	return d, true
}

// ---- the loop ------------------------------------------------------------

// watchdogTick is one round: find what dropped, answer it, move every cycle
// that is under way one step on.
func watchdogTick(conf Config) {
	limit, ok := watchdogCap(conf)
	if !ok {
		return
	}
	// The watchdog answers drops from the moment it was first switched on,
	// not the eleven months of history before it: those are the record, and
	// resuming them now would be a stampede of runs nobody asked for.
	since, known := wdMeta("enabled_at")
	if !known {
		since = wdNow()
		wdSetMeta("enabled_at", since)
	}
	watchWaiting(conf, limit)
	for _, run := range finishedSince(since) {
		f, err := readRunFacts(run)
		if err != nil {
			continue
		}
		d, _ := classifyDrop(f, conf)
		if d == nil {
			// Not a drop, and never will be: the row is finished. A marker so
			// it is not read again every tick.
			mu.Lock()
			db.Exec(`INSERT OR IGNORE INTO watchdog_meta (name, value) VALUES (?,?)`,
				"seen:"+run, wdNow())
			mu.Unlock()
			continue
		}
		answerDrop(d, conf, limit)
	}
	if handsOff(conf) {
		return
	}
	for _, c := range wdCycles(`state IN ('` + strings.Join(wdMoving, "','") + `')`) {
		stepCycle(c, conf, limit)
	}
}

// finishedSince is every run that ended after `since` and has not been read:
// neither recorded as a drop nor marked as seen.
func finishedSince(since float64) []string {
	mu.Lock()
	defer mu.Unlock()
	rows, err := db.Query(`SELECT run FROM runs WHERE finished IS NOT NULL AND finished >= ?
	                       AND run NOT IN (SELECT run FROM watchdog_drops)
	                       AND 'seen:' || run NOT IN (SELECT name FROM watchdog_meta)
	                       ORDER BY finished`, since)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if rows.Scan(&r) == nil {
			out = append(out, r)
		}
	}
	return out
}

// watchWaiting turns a run that has stopped getting on with it into a drop:
// on approval, on a lock nobody frees, on a background task that never
// reports. The run is ended by actions.on_watchdog_wait (stop_run unless
// written otherwise), so the resume that follows does not run beside it.
func watchWaiting(conf Config, limit float64) {
	fix := conf.str("watchdog", "fix_mode", "watchdogfix")
	for _, r := range openRuns() {
		if runMode(r.run) == fix {
			continue
		}
		if _, known := dropOfRun(r.run); known {
			continue
		}
		quiet, against, rule, phase, waiting := waitingOn(r.run, conf)
		if !waiting {
			continue
		}
		f, err := readRunFacts(r.run)
		if err != nil {
			continue
		}
		d := &dropFact{run: r.run, name: f.name, mode: f.mode, verdict: "WAITING",
			cause: "waiting", phase: resumeFrom(f)}
		if d.phase == "" {
			d.phase = phase
		}
		d.evidence = []string{
			fmt.Sprintf("run %s (%s, mode %s) open and waiting in phase %s", r.run, f.name,
				orDash(f.mode), orDash(phase)),
			fmt.Sprintf("no progress event (%s) for %.0fs against %s = %.0f",
				strings.Join(wdProgress, ","), quiet, rule, against),
		}
		action := conf.str("actions", "on_watchdog_wait", "stop_run")
		ctx := map[string]string{"run": r.run, "name": r.name, "phase": phase}
		for k, v := range conf["context"] {
			ctx[k] = v
		}
		judge("run", r.run, r.name, rule, against, quiet, action, act(action, conf, ctx))
		if endsRun(conf, action) {
			finish(r.run, "BLOCKED", fmt.Sprintf(
				"ammit watchdog: no progress for %.0fs in %s, past %s", quiet,
				orDash(phase), rule))
		}
		answerDrop(d, conf, limit)
	}
}

// answerDrop records a drop and starts the cycle that answers it, or says the
// chain is out of cycles.
func answerDrop(d *dropFact, conf Config, limit float64) {
	root, parent := rootOf(d.run)
	if !recordDrop(d, root) {
		return
	}
	if parent != 0 {
		wdMove(parent, wdAgain, "why=?", "the resumed run "+d.run+" dropped: "+d.cause)
	}
	text := strings.Join(d.evidence, "\n")
	judge("run", d.run, d.name, "watchdog.drop", limit, float64(spentCycles(root)),
		"watchdog", d.cause+" at "+orDash(d.phase))
	wdSay(d.run, d.phase, "detected", d.cause, spentCycles(root), limit, nil, text)
	startCycle(d, root, conf, limit)
}

// startCycle opens the next cycle of a chain on a drop, or announces that the
// chain has had every cycle it may take.
func startCycle(d *dropFact, root string, conf Config, limit float64) {
	for _, c := range wdCycles(`root=? AND state IN ('`+strings.Join(wdMoving, "','")+`')`, root) {
		// One cycle per chain at a time. A drop that arrives while one is
		// moving is the moving one's business: it is recorded, not answered.
		log.Printf("ammit: watchdog %s: cycle %d of %s is still %s", d.run, c.cycle, root, c.state)
		return
	}
	n := spentCycles(root) + 1
	now := wdNow()
	if float64(n) > limit {
		mu.Lock()
		db.Exec(`INSERT INTO watchdog_cycles (root, run, cycle, state, branch, why, started, updated)
		         VALUES (?,?,?,?,?,?,?,?)`, root, d.run, n, wdExhausted, "",
			fmt.Sprintf("loops.laps_watchdog = %.0f cycles spent", limit), now, now)
		mu.Unlock()
		announceExhausted(d, root, conf, limit)
		return
	}
	if handsOff(conf) {
		// Recorded, not answered, and no cycle spent on it: the switch says
		// hands off, and a cycle is a hand.
		wdSay(d.run, d.phase, "recorded", d.cause, n-1, limit, nil,
			"[hands off] the drop is recorded and no fix was queued: actions.enforce is off")
		return
	}
	branch := fmt.Sprintf("watchdog/%s-%d", shortRun(root), n)
	mu.Lock()
	res, err := db.Exec(`INSERT INTO watchdog_cycles (root, run, cycle, state, branch, started,
	                       entered, updated) VALUES (?,?,?,?,?,?,?,?)`,
		root, d.run, n, wdFixing, branch, now, now, now)
	mu.Unlock()
	if err != nil {
		log.Printf("ammit: watchdog could not open a cycle for %s: %v", d.run, err)
		return
	}
	id, _ := res.LastInsertId()
	queueFix(id, d, root, n, branch, conf, limit)
}

func shortRun(run string) string {
	if len(run) > 8 {
		return run[:8]
	}
	return run
}

// queueFix puts the fix run in the queue, behind whatever is live: it is a
// run like any other, through the same start command and the same
// queue.parallel. Its task names the branch its PR must be on - that branch
// is the only thing about it this service will look for afterwards.
func queueFix(id int64, d *dropFact, root string, n int, branch string, conf Config, limit float64) {
	key := fmt.Sprintf("WD-%s-%d", shortRun(root), n)
	task := fixTask(d, root, n, branch, conf)
	payload, _ := json.Marshal(map[string]string{
		"key": key, "mode": conf.str("watchdog", "fix_mode", "watchdogfix"), "task": task})
	mu.Lock()
	tx, err := db.BeginTx(context.Background(), nil)
	if err == nil {
		var res sql.Result
		res, err = tx.ExecContext(context.Background(), `INSERT INTO queue (name, payload, requested) VALUES (?,?,?)`,
			key, string(payload), wdNow())
		if err == nil {
			q, _ := res.LastInsertId()
			_, err = tx.ExecContext(context.Background(), `UPDATE watchdog_cycles SET fix_queue=?, updated=? WHERE id=?`,
				q, wdNow(), id)
		}
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
	}
	mu.Unlock()
	if err != nil {
		wdMove(id, wdFailed, "why=?", "could not queue the fix: "+err.Error())
		return
	}
	wdSay(d.run, d.phase, wdFixing, d.cause, n, limit, nil,
		fmt.Sprintf("fix run %s queued; its PR goes on branch %s", key, branch))
}

// fixTask is what the fix run is told. No quote and no brace reaches it from
// the record: it travels through a shell template, and a hole check that
// reads {word} as a placeholder.
func fixTask(d *dropFact, root string, n int, branch string, conf Config) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Watchdog fix, cycle %d of the chain that began with run %s.\n", n, root)
	fmt.Fprintf(&b, "Run %s of %s (mode %s) dropped at phase %s. Cause: %s.\n",
		d.run, d.name, orDash(d.mode), orDash(d.phase), d.cause)
	b.WriteString("Evidence from the record:\n")
	for _, line := range d.evidence {
		if len(line) > 600 {
			line = line[:600]
		}
		b.WriteString("- " + line + "\n")
	}
	for _, c := range wdCycles(`root=? AND cycle < ?`, root, n) {
		fmt.Fprintf(&b, "Earlier cycle %d (%s): %s %s\n", c.cycle, c.state, c.why,
			strings.Join(c.prs, " "))
	}
	if url := conf.str("context", "ammit", ""); url != "" {
		fmt.Fprintf(&b, "Full history: GET %s/watchdog?run=%s\n", url, root)
	}
	fmt.Fprintf(&b, "Fix the cause in code, config, card or stand and open ONE pull request "+
		"per repository on head branch %s. Never edit the dropped run's artefacts and never "+
		"weaken a gate.\n", branch)
	return strings.NewReplacer("'", "’", "{", "(", "}", ")").Replace(b.String())
}

// ---- moving a cycle on ---------------------------------------------------

// prView is one PR as `gh pr list --json url,state,mergedAt,mergeCommit,statusCheckRollup`
// prints it.
type prView struct {
	URL         string `json:"url"`
	State       string `json:"state"`
	MergedAt    string `json:"mergedAt"`
	MergeCommit *struct {
		Oid string `json:"oid"`
	} `json:"mergeCommit"`
	Checks []struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		State      string `json:"state"`
	} `json:"statusCheckRollup"`
}

// ci is a PR's checks as one word: green, red or pending. No checks at all is
// pending: a PR that nothing has checked is not a PR anything has proved.
func (p prView) ci() string {
	if len(p.Checks) == 0 {
		return "pending"
	}
	pending := false
	for _, c := range p.Checks {
		if c.State != "" { // a commit status, not a check run
			switch strings.ToUpper(c.State) {
			case "SUCCESS":
			case "PENDING", "EXPECTED":
				pending = true
			default:
				return "red"
			}
			continue
		}
		if strings.ToUpper(c.Status) != "COMPLETED" {
			pending = true
			continue
		}
		switch strings.ToUpper(c.Conclusion) {
		case "SUCCESS", "NEUTRAL", "SKIPPED":
		default:
			return "red"
		}
	}
	if pending {
		return "pending"
	}
	return "green"
}

// command runs a configured command and hands back everything it printed.
// act() keeps 200 bytes of an answer for the judgement line; a PR listing is
// data, and data cut at 200 bytes is not JSON.
func command(name string, conf Config, ctx map[string]string) (string, error) {
	tmpl := conf.str("commands", name, "")
	if tmpl == "" {
		return "", errors.New("no command named " + name)
	}
	for key, value := range ctx {
		tmpl = strings.ReplaceAll(tmpl, "{"+key+"}", shellSafe(value))
	}
	if hole := placeholder.FindString(tmpl); hole != "" {
		return "", errors.New(hole + " was never filled in " + name)
	}
	if dryRun {
		return "", errors.New("[dry run] " + tmpl)
	}
	out, err := exec.CommandContext(context.Background(), "sh", "-c", tmpl).Output() //nolint:gosec // a command limits.yml names, run as act() runs one
	return string(out), err
}

// prsOn asks GitHub, through commands.watchdog_pr, for every PR on a branch.
// The command may print one JSON array per repository; they are read one
// after another.
func prsOn(branch string, conf Config) ([]prView, error) {
	ctx := map[string]string{"branch": branch}
	for k, v := range conf["context"] {
		ctx[k] = v
	}
	out, err := command("watchdog_pr", conf, ctx)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(strings.NewReader(out))
	var all []prView
	for {
		var page []prView
		if err := dec.Decode(&page); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("watchdog_pr printed something that is not a JSON list: %w", err)
		}
		all = append(all, page...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].URL < all[j].URL })
	return all, nil
}

func urlsOf(prs []prView) []string {
	out := make([]string, 0, len(prs))
	for _, p := range prs {
		out = append(out, p.URL)
	}
	return out
}

// over says whether a cycle has spent longer in its state than the named
// timeout allows. No timeout set, never.
func over(c wdCycle, conf Config, key string, since float64) bool {
	limit, ok := conf.num("timeouts", key)
	return ok && limit > 0 && wdNow()-since > limit
}

func stepCycle(c wdCycle, conf Config, limit float64) {
	d, ok := dropOfRun(c.run)
	if !ok {
		return
	}
	fail := func(why string) {
		wdMove(c.id, wdFailed, "why=?", why)
		wdSay(c.run, d.phase, wdFailed, d.cause, c.cycle, limit, c.prs, why)
		judge("run", c.run, d.name, "watchdog.cycle", limit, float64(c.cycle), "failed", why)
		// A cycle that did not reach a resume is spent; the drop is still
		// there, so the next cycle answers the same drop.
		startCycle(d, c.root, conf, limit)
	}
	since := c.entered
	switch c.state {
	case wdFixing:
		prs, err := prsOn(c.branch, conf)
		if err != nil {
			log.Printf("ammit: watchdog %s: %v", c.branch, err)
		}
		if len(prs) > 0 {
			wdMove(c.id, wdCI, "prs=?", jsonList(urlsOf(prs)))
			wdSay(c.run, d.phase, wdCI, d.cause, c.cycle, limit, urlsOf(prs),
				"fix opened "+strings.Join(urlsOf(prs), " "))
			return
		}
		if ended, run := fixEnded(c); ended {
			fail(fmt.Sprintf("the fix run %s ended without a PR on %s", orDash(run), c.branch))
			return
		}
		if over(c, conf, "watchdog_fix", since) {
			fail("no PR on " + c.branch + " within timeouts.watchdog_fix")
		}
	case wdCI:
		prs, err := prsOn(c.branch, conf)
		if err != nil || len(prs) == 0 {
			if over(c, conf, "watchdog_ci", since) {
				fail("could not read the PRs on " + c.branch + " within timeouts.watchdog_ci")
			}
			return
		}
		urls := urlsOf(prs)
		for _, p := range prs {
			if strings.ToUpper(p.State) == "CLOSED" {
				fail("PR " + p.URL + " was closed without merging")
				return
			}
		}
		green := true
		for _, p := range prs {
			if strings.ToUpper(p.State) == "MERGED" {
				continue
			}
			switch p.ci() {
			case "red":
				// The fix run opens its PR and goes on working: on APF-3296
				// both WD-2ca50154-1 and -2 pushed a lint fix after their
				// first push went red, and the cycle had already been failed
				// on that first red - a green PR abandoned, a second and third
				// cycle spent fixing what was fixed. Red counts once the run
				// that can still push has ended.
				if ended, _ := fixEnded(c); !ended {
					green = false
					continue
				}
				fail("CI is red on " + p.URL)
				return
			case "pending":
				green = false
			}
		}
		if !green {
			if over(c, conf, "watchdog_ci", since) {
				fail("CI did not finish green within timeouts.watchdog_ci on " + strings.Join(urls, " "))
			}
			return
		}
		said := []string{}
		for _, p := range prs {
			if strings.ToUpper(p.State) == "MERGED" {
				continue
			}
			out := act("watchdog_merge", conf, map[string]string{"pr": p.URL, "run": c.run,
				"name": d.name})
			said = append(said, p.URL+": "+out)
		}
		wdMove(c.id, wdMerging, "prs=?", jsonList(urls))
		wdSay(c.run, d.phase, wdMerging, d.cause, c.cycle, limit, urls,
			"CI green; merge asked: "+strings.Join(said, "; "))
	case wdMerging:
		prs, err := prsOn(c.branch, conf)
		if err != nil || len(prs) == 0 {
			if over(c, conf, "watchdog_ci", since) {
				fail("could not read the PRs on " + c.branch + " to see them merged")
			}
			return
		}
		latest := 0.0
		for _, p := range prs {
			if strings.ToUpper(p.State) != "MERGED" {
				if strings.ToUpper(p.State) == "CLOSED" {
					fail("PR " + p.URL + " was closed without merging")
					return
				}
				if over(c, conf, "watchdog_ci", since) {
					fail("PR " + p.URL + " is still not merged")
				}
				return
			}
			if at, err := time.Parse(time.RFC3339, p.MergedAt); err == nil {
				if v := float64(at.UnixNano()) / 1e9; v > latest {
					latest = v
				}
			}
		}
		if latest == 0 {
			latest = wdNow()
		}
		// Never earlier than the cycle: a deploy that predates the fix cannot
		// have carried it, whatever a clock on GitHub's side says.
		if latest < c.started {
			latest = c.started
		}
		wdMove(c.id, wdDeploying, "merged_at=?", latest)
		said := ""
		if conf.str("commands", "watchdog_deploy", "") != "" {
			said = "; deploy asked: " + act("watchdog_deploy", conf, map[string]string{
				"pr": strings.Join(urlsOf(prs), " "), "run": c.run, "name": d.name})
		}
		wdSay(c.run, d.phase, wdDeploying, d.cause, c.cycle, limit, urlsOf(prs),
			"merged "+strings.Join(urlsOf(prs), " ")+said)
	case wdDeploying:
		at, ok := deployedAfter(c.mergedAt.Float64)
		if ok && conf.str("commands", "watchdog_deployed", "") != "" {
			ctx := map[string]string{"pr": strings.Join(c.prs, " "), "run": c.run, "name": d.name}
			for k, v := range conf["context"] {
				ctx[k] = v
			}
			if _, err := command("watchdog_deployed", conf, ctx); err != nil {
				ok = false
			}
		}
		if !ok {
			if over(c, conf, "watchdog_deploy", c.mergedAt.Float64) {
				fail("no deploy reported success after the merge within timeouts.watchdog_deploy")
			}
			return
		}
		q := queueResume(c, d)
		wdMove(c.id, wdResumed, "deployed_at=?, resume_queue=?", at, q)
		wdSay(c.run, d.phase, wdResumed, d.cause, c.cycle, limit, c.prs, fmt.Sprintf(
			"deployed at %s; %s queued to resume from %s (source run %s)",
			time.Unix(int64(at), 0).UTC().Format(time.RFC3339), d.name, orDash(d.phase), c.run))
	case wdResumed:
		run, finished, verdict, started := resumeOutcome(c)
		if run == "" {
			if started > 0 && over(c, conf, "watchdog_start", started) {
				fail("the resume was started and no run came of it within timeouts.watchdog_start")
			}
			return
		}
		if c.resumedRun == "" {
			wdUpdate(c.id, "resumed_run=?", run)
		}
		if !finished {
			return
		}
		if wdGreen[strings.ToUpper(verdict)] {
			wdMove(c.id, wdDone, "why=?", "the resumed run "+run+" ended "+verdict)
			wdSay(run, d.phase, wdDone, d.cause, c.cycle, limit, c.prs,
				"the resumed run ended "+verdict)
			return
		}
		if _, dropped := dropOfRun(run); !dropped {
			// Read next tick by the drop detector; only a run it will not
			// call a drop (a person stopped it, a later run superseded it)
			// ends the chain here.
			f, err := readRunFacts(run)
			if err != nil {
				return
			}
			if again, why := classifyDrop(f, conf); again == nil {
				wdMove(c.id, wdLeft, "why=?", "the resumed run "+run+": "+why)
				wdSay(run, d.phase, wdLeft, d.cause, c.cycle, limit, c.prs,
					"the resumed run is not a drop: "+why)
			}
		}
	}
}

func jsonList(v []string) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// fixEnded says whether the fix run this cycle queued has finished.
func fixEnded(c wdCycle) (bool, string) {
	if !c.fixQueue.Valid {
		return false, ""
	}
	mu.Lock()
	defer mu.Unlock()
	var run sql.NullString
	var finished sql.NullFloat64
	db.QueryRow(`SELECT q.run, r.finished FROM queue q LEFT JOIN runs r ON r.run = q.run
	             WHERE q.id=?`, c.fixQueue.Int64).Scan(&run, &finished)
	return finished.Valid, run.String
}

// deployedAfter is the first deploy that reported success after the merge.
// The deploy job reports how it ended (POST /deploy); a merge is live once a
// deploy that started after it finished well.
func deployedAfter(merged float64) (float64, bool) {
	mu.Lock()
	defer mu.Unlock()
	var at float64
	err := db.QueryRow(`SELECT at FROM deploys WHERE status='success' AND at > ?
	                    ORDER BY at LIMIT 1`, merged).Scan(&at)
	return at, err == nil
}

// queueResume queues the dropped ticket to continue from the phase that
// dropped, with the dropped run as the source of its artefacts. Through the
// queue, so it waits behind any live run: the runner has one clone.
func queueResume(c wdCycle, d *dropFact) int64 {
	body := map[string]string{"key": d.name, "rerun": "resume", "source_run": c.run}
	if d.mode != "" {
		body["mode"] = d.mode
	}
	if d.phase != "" {
		body["from_phase"] = d.phase
	}
	payload, _ := json.Marshal(body)
	mu.Lock()
	defer mu.Unlock()
	res, err := db.Exec(`INSERT INTO queue (name, payload, requested) VALUES (?,?,?)`,
		d.name, string(payload), wdNow())
	if err != nil {
		return 0
	}
	id, _ := res.LastInsertId()
	return id
}

// resumeOutcome is the run the resume turned into, whether it has finished
// and how, and when the queue started it (0 while it waits its turn).
func resumeOutcome(c wdCycle) (string, bool, string, float64) {
	if !c.resumeQueue.Valid {
		return "", false, "", 0
	}
	mu.Lock()
	defer mu.Unlock()
	var run, verdict sql.NullString
	var started, finished sql.NullFloat64
	db.QueryRow(`SELECT q.run, q.started, r.finished, r.verdict FROM queue q
	             LEFT JOIN runs r ON r.run = q.run WHERE q.id=?`, c.resumeQueue.Int64).
		Scan(&run, &started, &finished, &verdict)
	return run.String, finished.Valid, verdict.String, started.Float64
}

// announceExhausted is the loud end of a chain: a judgement, an event with
// every cycle in it, and the notify command.
func announceExhausted(d *dropFact, root string, conf Config, limit float64) {
	cycles := wdCycles(`root=?`, root)
	lines := make([]string, 0, len(cycles))
	for _, c := range cycles {
		if c.state == wdExhausted {
			continue
		}
		dr, _ := dropOfRun(c.run)
		cause, phase := "", ""
		if dr != nil {
			cause, phase = dr.cause, dr.phase
		}
		lines = append(lines, fmt.Sprintf("cycle %d: drop %s (%s at %s) -> %s %s %s",
			c.cycle, shortRun(c.run), cause, orDash(phase), c.state, c.why, strings.Join(c.prs, " ")))
	}
	history := strings.Join(lines, "\n")
	why := fmt.Sprintf("WATCHDOG EXHAUSTED: %s dropped again (%s at %s) after %.0f cycle(s), "+
		"loops.laps_watchdog = %.0f", d.name, d.cause, orDash(d.phase), limit, limit)
	wdSay(d.run, d.phase, wdExhausted, d.cause, int(limit), limit, nil, why+"\n"+history)
	judge("run", d.run, d.name, "loops.laps_watchdog", limit, limit+1, "notify", why)
	ctx := map[string]string{"run": d.run, "name": d.name, "verdict": "WATCHDOG-EXHAUSTED",
		"phase": d.phase}
	for k, v := range conf["context"] {
		ctx[k] = v
	}
	summary := why + " | " + strings.ReplaceAll(history, "\n", " | ")
	ctx["summary"] = strings.NewReplacer("'", "’", "{", "(", "}", ")").Replace(summary)
	log.Printf("ammit: notify: %s", act("notify", conf, ctx))
}

// watchdogState is GET /watchdog: every drop and every cycle, or one chain's
// when ?run= names any run in it. What the fix run reads for its history.
func watchdogState(run string) map[string]any {
	root := run
	if run != "" {
		mu.Lock()
		db.QueryRow(`SELECT root FROM watchdog_drops WHERE run=?`, run).Scan(&root)
		mu.Unlock()
	}
	where, args := "1=1", []any{}
	if root != "" {
		where, args = "root=?", []any{root}
	}
	mu.Lock()
	defer mu.Unlock()
	return map[string]any{"drops": wdDropRows(where, args), "cycles": wdCycleRows(where, args)}
}

func wdDropRows(where string, args []any) []map[string]any {
	drops := []map[string]any{}
	rows, err := db.Query(`SELECT run, root, name, mode, phase, cause, verdict, evidence, at
	                    FROM watchdog_drops WHERE `+where+` ORDER BY at DESC LIMIT 200`, args...)
	if err != nil {
		return drops
	}
	defer rows.Close()
	{
		for rows.Next() {
			var run, root, name, mode, phase, cause, verdict, ev sql.NullString
			var at float64
			if rows.Scan(&run, &root, &name, &mode, &phase, &cause, &verdict, &ev, &at) == nil {
				var lines []string
				_ = json.Unmarshal([]byte(ev.String), &lines)
				drops = append(drops, map[string]any{"run": run.String, "root": root.String,
					"name": name.String, "mode": mode.String, "phase": phase.String,
					"cause": cause.String, "verdict": verdict.String, "evidence": lines, "at": at})
			}
		}
	}
	return drops
}

func wdCycleRows(where string, args []any) []map[string]any {
	cycles := []map[string]any{}
	rows, err := db.Query(`SELECT id, root, run, cycle, state, branch, coalesce(prs,'[]'),
	                      merged_at, deployed_at, coalesce(resumed_run,''), coalesce(why,''),
	                      started, updated
	                    FROM watchdog_cycles WHERE `+where+` ORDER BY id DESC LIMIT 200`, args...)
	if err != nil {
		return cycles
	}
	defer rows.Close()
	{
		for rows.Next() {
			var id int64
			var cycle int
			var root, run, state, branch, prs, resumed, why string
			var merged, deployed sql.NullFloat64
			var started, updated float64
			if rows.Scan(&id, &root, &run, &cycle, &state, &branch, &prs, &merged, &deployed,
				&resumed, &why, &started, &updated) == nil {
				var list []string
				_ = json.Unmarshal([]byte(prs), &list)
				c := map[string]any{"id": id, "root": root, "run": run, "cycle": cycle,
					"state": state, "branch": branch, "prs": list, "resumed_run": resumed,
					"why": why, "started": started, "updated": updated}
				if merged.Valid {
					c["merged_at"] = merged.Float64
				}
				if deployed.Valid {
					c["deployed_at"] = deployed.Float64
				}
				cycles = append(cycles, c)
			}
		}
	}
	return cycles
}
