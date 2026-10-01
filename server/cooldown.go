package main

import (
	"fmt"
	"strings"
)

// The stack between runs.
//
// The product under test is the stack's whole memory and most of its CPU, and
// none of that is spent on the pipeline: on 1 October two of its containers
// held five cores and the host was out of memory with no run in flight and
// none queued. Nothing stops them, because the thing that starts them - envd's
// prepare, on every dispatch - is the only thing that ever thinks about them.
//
// So this is the other half of prepare. Ten minutes (timeouts.idle) with no run
// open, nothing waiting and nothing being dispatched is a cooldown: the product
// is put down and the daemon is pruned (actions.on_idle, commands.cooldown).
// prepare brings the product back up on the next dispatch, so there is no
// warm-up to pair this with - the pairing already exists.
//
// Judged once per idle stretch, keyed on the last start it saw: a stretch is
// one fact, not one fact per tick. A command that failed (envd down, daemon
// away) is tried again one idle period later, because an idle stack that was
// not put down is still the fault this exists for.
//
// Measured awake, like every age here: the stretch is counted from the later
// of the last start and our own uptime, so a restart or a sleep does not
// put the product down on a silence nobody witnessed.

var cooledFor = -1.0

func cooldown(conf Config) {
	idle, ok := conf.num("timeouts", "idle")
	if !ok || idle <= 0 {
		return
	}
	if len(openRuns()) > 0 {
		return
	}
	var pending int
	var lastRun, lastQueued float64
	mu.Lock()
	db.QueryRow(`SELECT count(*) FROM queue WHERE state IN ('waiting','running')`).
		Scan(&pending)
	db.QueryRow(`SELECT coalesce(max(started),0) FROM runs`).Scan(&lastRun)
	db.QueryRow(`SELECT coalesce(max(coalesce(started,requested)),0) FROM queue`).
		Scan(&lastQueued)
	mu.Unlock()
	if pending > 0 {
		return
	}
	last := lastRun
	if lastQueued > last {
		last = lastQueued
	}
	since := nowWall() - last
	if awake := nowWall() - _upSince; awake < since {
		since = awake
	}
	if since <= idle || cooledFor == last {
		return
	}
	key := fmt.Sprintf("%.0f", last)
	if recently("timeouts.idle", key, idle) {
		return
	}
	action := conf.str("actions", "on_idle", "cooldown")
	ctx := map[string]string{"name": "stack", "run": "-"}
	for k, v := range conf["context"] {
		ctx[k] = v
	}
	outcome := act(action, conf, ctx)
	judge("stack", "", "product", "timeouts.idle", idle, since, action, outcome)
	if !strings.HasPrefix(outcome, "failed") {
		cooledFor = last
	}
}
