package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	_ "modernc.org/sqlite"
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
	         WHERE state='running' AND ifnull(run,'') <> ''
	           AND run IN (SELECT run FROM runs WHERE finished IS NOT NULL)`, now)
	// Never tied to anything, and older than a run is allowed to take. Whatever
	// it was, it is not running now.
	if limit, ok := conf.num("timeouts", "run"); ok && limit > 0 {
		db.Exec(`UPDATE queue SET state='done', finished=?
		         WHERE state='running' AND ifnull(run,'') = ''
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
		rows.Close()
	}
	mu.Unlock()
	now := float64(time.Now().UnixNano()) / 1e9
	for _, q := range found {
		silent := now - q.at
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
	                           WHERE state='running' AND ifnull(run,'') = '' AND started > ?`,
		float64(time.Now().Add(-3*time.Minute).UnixNano())/1e9); err == nil {
		for rows.Next() {
			var p string
			if rows.Scan(&p) == nil {
				starting[standOf(p)]++
			}
		}
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
		rows.Close()
	}
	// Not into a deploy that is going or waiting (deploys.go, deployClear):
	// the item stays waiting, rather than being started, refused by the
	// orchestrator, and left running with no run behind it. The deploy
	// replaces the stack's containers; a host run rides it out.
	deployOK, _ := lastDeployClear()
	mu.Unlock()
	dockerFree := float64(docker) < slots && deployOK
	hostFree := host != "" && onHost == 0
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
	if !strings.Contains(outcome, `"run_id"`) {
		mu.Lock()
		db.Exec(`UPDATE queue SET state='done', finished=? WHERE id=? AND ifnull(run,'') = ''`,
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
