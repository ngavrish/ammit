package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strings"
	"time"
)

// The queue and the sweep: what starts next, what was abandoned.

// sweepAbandoned closes runs nothing has reported on for longer than a run is
// allowed to take.
//
// Every rule works from openRuns, which asks for runs started in the last
// seventy-two hours. That window was meant to keep the loop off ancient rows,
// and it also means a row still open after seventy-two hours can never be
// closed by anything: not judged, not timed out, not swept. Twenty of them were
// sitting in this database, one genuinely open and nineteen that had ended
// without the event that says so.
//
// Silence is the signal, not age. A run reports constantly while it lives — a
// turn, a call, a phase — so nothing heard for longer than timeouts.run means
// the run is not running, whether it was killed, whether its worker was
// replaced, or whether this service was down when it ended. It is closed as
// what it is: abandoned, not finished, and never confused with a verdict
// somebody's work produced.
// sweepQueue closes queue rows whose run is over, and rows that never became a
// run at all.
//
// A row at "running" holds a slot for ever, and the slot is the whole of what
// queue.parallel controls. Two ways it happens: the run it became has finished
// and the row was never tied to it, or the start failed and nothing ever came
// back. Both look the same from here and both end the same way.
func sweepQueue(conf Config) {
	mu.Lock()
	defer mu.Unlock()
	now := float64(time.Now().UnixNano()) / 1e9
	// Tied to a run that is over.
	db.Exec(`UPDATE queue SET state='done', finished=?
	         WHERE state='running' AND coalesce(run,'') <> ''
	           AND run IN (SELECT run FROM runs WHERE finished IS NOT NULL)`, now)
	// Never tied to anything, and older than a run is allowed to take. Whatever
	// it was, it is not running now.
	if limit, ok := conf.num("timeouts", "run"); ok && limit > 0 {
		db.Exec(`UPDATE queue SET state='done', finished=?
		         WHERE state='running' AND coalesce(run,'') = ''
		           AND coalesce(started, requested) < ?`, now, now-limit)
	}
}

func sweepAbandoned(conf Config) {
	limit, ok := conf.num("timeouts", "run")
	if !ok || limit <= 0 {
		return
	}
	mu.Lock()
	rows, err := db.Query(`SELECT r.run, coalesce(max(e.at), r.started)
	                       FROM runs r LEFT JOIN events e ON e.run = r.run
	                       WHERE r.finished IS NULL GROUP BY r.run`)
	type quiet struct {
		run string
		at  float64
	}
	var found []quiet
	if err == nil {
		for rows.Next() {
			var q quiet
			if rows.Scan(&q.run, &q.at) == nil {
				found = append(found, q)
			}
		}
		_ = rows.Err() // a cut-off read is the shorter answer it always was
		rows.Close()
	}
	mu.Unlock()
	// Only the silence measured AWAKE counts, as in weigh and sweepCrashed.
	// Run 4ef880b4 (2026-11-05) was closed ABANDONED at 09:20:17 for "301
	// minutes" of silence that was the host's sleep: ammit's own round had
	// written its limits at 04:18:55, the VM froze inside that round, and on
	// waking this sweep read the whole night as the run's. Nothing in ammit's
	// tables - no event, no sample, no limit, from any run - is dated between
	// those two seconds, and the run's runner beat again 49s after it was
	// closed. A run that really did go quiet while we slept still gets here,
	// one timeouts.run after we woke, and a dead worker much sooner through
	// weigh's heartbeat check.
	now := nowWall()
	up := now - upSince()
	for _, q := range found {
		silent := math.Min(now-q.at, up)
		if silent <= limit {
			continue
		}
		finish(q.run, "ABANDONED", fmt.Sprintf(
			"nothing has reported on this run for %.0f minutes, which is longer "+
				"than timeouts.run — closed as abandoned, not as finished",
			silent/60))
	}
}

// queueStart is how pumpQueue starts an item: act, unless a test stands in.
var queueStart = act

// pumpQueue starts the next item when a stand frees, in order.
//
// Parallelism becomes a number in a file rather than a property of whoever
// pressed the button first.
//
// One line for both stands. Each stand has one clone to test in - the docker
// runner's and the host's - and a start that went straight to a stand's
// orchestrator waited inside its runner for that clone, out of sight, while
// the other stand sat idle: APF-3469 (2026-10-30) waited behind APF-3122 on
// the host for as long as it was allowed to, with the docker stand free.
// Every start comes here now (the orchestrators queue what they are asked),
// and an item goes to whichever stand is free when its turn comes. An item
// that names its stand (place, or where = local) waits for that one; the line
// behind it does not.
func pumpQueue(conf Config) {
	pumpQueueSkipping(conf, map[string]bool{})
}

// pumpQueueSkipping starts the first waiting item whose stand is free and
// not in `held`. A start the stack holds off marks its stand held for this
// pass and the pass goes on to the next item: APF-3122's host resume (row
// 543, 2026-11-03) waited behind WD-a20dcd5d-1, refused every tick on docker
// ("ui-regression holds the stack"), with the host free.
func pumpQueueSkipping(conf Config, held map[string]bool) {
	slots := 1.0
	if v, ok := conf.num("queue", "parallel"); ok {
		slots = v
	}
	host := conf.str("context", "host_starter", "")
	mu.Lock()
	since := float64(time.Now().Add(-24*time.Hour).UnixNano()) / 1e9
	// A run on the host (its run_start says tags.where = local) holds the
	// host's clone, not the runner's: counted as a docker slot, the APF-2327
	// host run on 2026-10-12 would have held APF-3296's queue the way its
	// stack run had for two hours the night before.
	var docker, onHost int
	db.QueryRow(`SELECT count(*) FROM runs r WHERE r.finished IS NULL AND r.started > ?
	               AND NOT EXISTS (SELECT 1 FROM events e WHERE e.run = r.run
	                 AND e.kind = 'run_start'
	                 AND `+whereOf("e.payload")+` = 'local')`, since).Scan(&docker)
	db.QueryRow(`SELECT count(*) FROM runs r WHERE r.finished IS NULL AND r.started > ?
	               AND EXISTS (SELECT 1 FROM events e WHERE e.run = r.run
	                 AND e.kind = 'run_start'
	                 AND `+whereOf("e.payload")+` = 'local')`, since).Scan(&onHost)
	// A start that has not reported its run yet holds its stand too: between
	// the start and its run_start the stand looks free for a tick, and a
	// second item sent into it then waits in its runner for the clone.
	starting := map[string]int{}
	if rows, err := db.Query(`SELECT coalesce(payload,'') FROM queue
	                           WHERE state='running' AND coalesce(run,'') = '' AND started > ?`,
		float64(time.Now().Add(-3*time.Minute).UnixNano())/1e9); err == nil {
		for rows.Next() {
			var p string
			if rows.Scan(&p) == nil {
				starting[standOf(p)]++
			}
		}
		_ = rows.Err() // a cut-off read is the shorter answer it always was
		rows.Close()
	}
	docker += starting["docker"] + starting[""]
	onHost += starting["local"]
	type item struct {
		id            int64
		name, payload string
	}
	var waiting []item
	if rows, err := db.Query(`SELECT id, name, coalesce(payload,'') FROM queue
	                           WHERE state='waiting' ORDER BY requested LIMIT 100`); err == nil {
		for rows.Next() {
			var it item
			if rows.Scan(&it.id, &it.name, &it.payload) == nil {
				waiting = append(waiting, it)
			}
		}
		_ = rows.Err() // a cut-off read is the shorter answer it always was
		rows.Close()
	}
	// Not into a deploy that is going or waiting (deploys.go, deployClear):
	// the item stays waiting, rather than being started, refused by the
	// orchestrator, and left running with no run behind it. The deploy
	// replaces the stack's containers; a host run rides it out.
	deployOK, _ := lastDeployClear()
	// A stand that runs claim branches of a run on the other stand is busy
	// as if that run were on it: the runner there holds `branch-<stand>`
	// while the job has its clone and its product (agent-runner
	// branch_share.py). One run's fan-out spreads over both stands; a start
	// sent into a stand doing that would wait in its runner, out of sight.
	branchDocker, branchLocal := leaseHeld("branch-docker"), leaseHeld("branch-local")
	// Nothing starts, on either stand, while a deploy holds the `deploy`
	// lease. The deploy takes it before it looks for live runs and keeps it
	// until it has replaced what it replaces - ammit among them - so a start
	// cannot land between its look and its recreate. A host run used to start
	// regardless ("it rides the deploy out"), and it does not ride out ammit:
	// APF-3478's 2d1357cb was started from this queue at 15:19:26 on
	// 2026-11-04, a deploy that had just looked took the lease at 15:19:30,
	// and ammit was recreated under the run a minute later.
	deployLease := leaseHeld("deploy")
	releaseEndedStandLeases()
	mu.Unlock()
	dockerFree := float64(docker) < slots && deployOK && !deployLease && !held["docker"] && !branchDocker
	hostFree := host != "" && onHost == 0 && !deployLease && !held["local"] && !branchLocal
	var id int64
	var name, payload, stand string
	for _, it := range waiting {
		switch standOf(it.payload) {
		case "local":
			if hostFree {
				stand = "local"
			}
		case "docker":
			if dockerFree {
				stand = "docker"
			}
		default:
			if dockerFree {
				stand = "docker"
			} else if hostFree {
				stand = "local"
			}
		}
		if stand != "" {
			id, name, payload = it.id, it.name, it.payload
			break
		}
	}
	if stand == "" {
		return
	}
	// A resume that waited in line past the ticket's verdict is not wanted any
	// more: a run of the ticket that ended PASS or FAIL after the row was
	// queued answered it. 2026-10-27: a keeper resume of APF-3301 queued at
	// 11:46 started at 12:0x, after the ticket had ended FAIL at 11:51.
	if strings.Contains(payload, `"rerun":"resume"`) {
		var done int
		mu.Lock()
		db.QueryRow(`SELECT count(*) FROM runs WHERE name=? AND verdict IN ('PASS','FAIL')
		             AND finished > (SELECT requested FROM queue WHERE id=?)`, name, id).Scan(&done)
		if done > 0 {
			db.Exec(`UPDATE queue SET state='done', finished=? WHERE id=?`, nowWall(), id)
			mu.Unlock()
			log.Printf("ammit: queue row %d (%s) withdrawn: the ticket ended PASS/FAIL after it was queued", id, name)
			return
		}
		mu.Unlock()
	}
	asked := payload // the row as queued, without the stand this attempt picks
	payload = placed(payload, stand)
	ctx := map[string]string{"name": name, "payload": payload}
	for k, v := range conf["context"] {
		ctx[k] = v
	}
	// A run of the host stand starts on the host's orchestrator.
	if stand == "local" {
		ctx["starter"] = host
	}
	// The row is running before the run is started, not after. run_start ties
	// the oldest running, unlinked row of the ticket to the run it became, and
	// the run can report its start before the start command has returned: a
	// run_start that arrives inside that call finds the row still waiting and
	// ties nothing. WD-db62b413-2 (queue row 60) ran as e8b5fb60, whose
	// run_start is stamped 0.012 s before the row's started; the run ended,
	// its row stayed running with no run, and the watchdog failed the cycle as
	// "the fix run was started and no run came of it".
	mu.Lock()
	db.Exec(`UPDATE queue SET state='running', started=?, payload=? WHERE id=?`,
		float64(time.Now().UnixNano())/1e9, payload, id)
	mu.Unlock()
	busy := docker
	if stand == "local" {
		busy = onHost
	}
	outcome := queueStart(conf.str("actions", "on_start", "start_run"), conf, ctx)
	judge("queue", "", name, "queue.parallel", slots, float64(busy+1), "started on "+stand, outcome)
	// A start the orchestrator refused names no run, and nothing will ever tie
	// one to the row. Left running it held its stand: WD-b5ba15bd-2 (row 389,
	// 2026-10-31) was refused "no such ticket" and kept the docker stand from
	// APF-3469 until the row was deleted by hand, twice.
	// A refusal that asks to be tried again is not a refusal of the row: the
	// stack is held (a deploy, or a deploy lease such as a regression's) and
	// will be free later. Closed, the start was lost without a word:
	// ADHOC-20261102-145843 (row 508) was refused "ui-regression holds the
	// stack" and closed seven seconds after it was queued. It waits again.
	// It waits as it was queued: the stand this attempt picked goes back out
	// of the payload. Kept, one held-off try pinned an unpinned row to docker
	// for good, and the shared queue never sent it to an idle host
	// (ADHOC-20261103-075702 behind the UI regression's lease, 2026-11-03).
	// "already running" is the same: the ticket's last run is still closing.
	// 2026-11-06 00:38: the watchdog queued APF-1972's fresh-budget resume
	// three seconds after stopping fd5250ca, the orchestrator answered
	// "already running: APF-1972", the row was closed, and the ticket lay
	// stopped for eight hours.
	if !strings.Contains(outcome, `"run_id"`) &&
		(strings.Contains(outcome, "try again") || strings.Contains(outcome, "already running")) {
		mu.Lock()
		db.Exec(`UPDATE queue SET state='waiting', started=NULL, payload=? WHERE id=? AND coalesce(run,'') = ''`, asked, id)
		mu.Unlock()
		log.Printf("ammit: queue row %d (%s) held off on %s, waits again: %s", id, name, stand, outcome)
		held[stand] = true
		pumpQueueSkipping(conf, held)
		return
	}
	if !strings.Contains(outcome, `"run_id"`) {
		mu.Lock()
		db.Exec(`UPDATE queue SET state='done', finished=? WHERE id=? AND coalesce(run,'') = ''`,
			nowWall(), id)
		mu.Unlock()
		log.Printf("ammit: queue row %d (%s) refused on %s, closed: %s", id, name, stand, outcome)
	}
}

// standOf is the stand a queued start asks for, by its "place": "local" (the
// host), "docker", or "" for whichever is free. ("stand" in the same payload is
// the run's addresses on its stand, not this.)
func standOf(payload string) string {
	var p map[string]any
	if json.Unmarshal([]byte(payload), &p) != nil {
		return ""
	}
	if s, _ := p["place"].(string); s == "local" || s == "docker" {
		return s
	}
	if w, _ := p["where"].(string); w == "local" {
		return "local"
	}
	return ""
}

// placed is the payload as it is sent: on the stand chosen for it, and marked
// as coming from this queue, so the orchestrator starts it instead of queueing
// it again. A run sent to the host runs there (where = local).
func placed(payload, stand string) string {
	var p map[string]any
	if json.Unmarshal([]byte(payload), &p) != nil || p == nil {
		return payload
	}
	p["place"] = stand
	p["queued"] = true
	if stand == "local" {
		p["where"] = "local"
	}
	out, err := json.Marshal(p)
	if err != nil {
		return payload
	}
	return string(out)
}

// leaseHeld says whether the named lease is held and not expired. The
// caller holds mu.
// releaseEndedStandLeases gives back a stand lease ("stand-<where>", held by
// "<TICKET> <commit>") once its ticket has no run left that has not finished,
// however the run ended: a verdict, a stop, a stillborn start, an orphan the
// watchdog closed. Only the run itself released it before, at its own end,
// and a run that never reached its end kept the stand: on 2026-11-04 the host
// stand stayed ADHOC-20261103-075702's from 13:52 to 23:05. The caller holds mu.
func releaseEndedStandLeases() {
	rows, err := db.Query(`SELECT name, holder FROM leases WHERE name ILIKE 'stand-%'`)
	if err != nil {
		return
	}
	type lease struct{ name, holder string }
	var held []lease
	for rows.Next() {
		var l lease
		if rows.Scan(&l.name, &l.holder) == nil {
			held = append(held, l)
		}
	}
	_ = rows.Err() // a cut-off read is the shorter answer it always was
	rows.Close()
	for _, l := range held {
		ticket := strings.Fields(l.holder)
		if len(ticket) == 0 {
			continue
		}
		var live int
		db.QueryRow(`SELECT count(*) FROM runs WHERE name=? AND finished IS NULL`, ticket[0]).Scan(&live)
		if live == 0 {
			db.Exec(`DELETE FROM leases WHERE name=? AND holder=?`, l.name, l.holder)
			log.Printf("ammit: %s released - %s has no live run", l.name, ticket[0])
		}
	}
}

func leaseHeld(name string) bool {
	var n int
	db.QueryRow(`SELECT count(*) FROM leases WHERE name=? AND expires > ?`,
		name, float64(time.Now().UnixNano())/1e9).Scan(&n)
	return n > 0
}
