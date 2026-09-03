package main

import (
	"fmt"
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

// pumpQueue starts the next item when a slot frees, in order.
//
// Parallelism becomes a number in a file rather than a property of whoever
// pressed the button first.
func pumpQueue(conf Config) {
	slots := 1.0
	if v, ok := conf.num("queue", "parallel"); ok {
		slots = v
	}
	mu.Lock()
	var active int
	db.QueryRow(`SELECT count(*) FROM runs WHERE finished IS NULL AND started > ?`,
		float64(time.Now().Add(-24*time.Hour).UnixNano())/1e9).Scan(&active)
	var id int64
	var name, payload string
	err := db.QueryRow(`SELECT id, name, coalesce(payload,'') FROM queue
	                    WHERE state='waiting' ORDER BY requested LIMIT 1`).
		Scan(&id, &name, &payload)
	mu.Unlock()
	if err != nil || float64(active) >= slots {
		return
	}
	ctx := map[string]string{"name": name, "payload": payload}
	for k, v := range conf["context"] {
		ctx[k] = v
	}
	outcome := act(conf.str("actions", "on_start", "start_run"), conf, ctx)
	mu.Lock()
	db.Exec(`UPDATE queue SET state='running', started=? WHERE id=?`,
		float64(time.Now().UnixNano())/1e9, id)
	mu.Unlock()
	judge("queue", "", name, "queue.parallel", slots, float64(active+1), "started", outcome)
}
