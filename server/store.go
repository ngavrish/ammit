package main

import (
	"encoding/json"
	"log"
	"time"

	_ "modernc.org/sqlite"
)

// What an event becomes when it lands.

type event map[string]any

func (e event) s(key string) string {
	if v, ok := e[key].(string); ok {
		return v
	}
	return ""
}

func (e event) f(key string) float64 {
	if v, ok := e[key].(float64); ok {
		return v
	}
	return 0
}

func store(e event) {
	mu.Lock()
	defer mu.Unlock()
	at := e.f("at")
	if at == 0 {
		at = float64(time.Now().UnixNano()) / 1e9
	}
	payload, _ := json.Marshal(e)
	res, err := db.Exec(
		`INSERT INTO events (at, kind, run, phase, session, agent, branch, payload)
		 VALUES (?,?,?,?,?,?,?,?)`,
		at, e.s("kind"), e.s("run"), e.s("phase"), e.s("session"), e.s("agent"),
		e.s("branch"), string(payload))
	if err != nil {
		log.Printf("ammit: could not keep an event: %v", err)
		return
	}
	if id, err := res.LastInsertId(); err == nil {
		lift(id, at, e)
	}
	// A run this store has never heard of gets a row on its first event.
	//
	// The start event travels the same wire as everything else, and that wire
	// blinks: a DNS hiccup at the wrong second dropped one, and a run with no row
	// is a run no limit applies to — invisible to the cost cap, to the deadline,
	// to the queue's idea of what is running. The row is what makes a run real
	// here, so any event may create it and run_start merely fills in the name.
	if run := e.s("run"); run != "" && e.s("kind") != "run_start" {
		db.Exec(`INSERT OR IGNORE INTO runs (run, name, started) VALUES (?,?,?)`,
			run, run, at)
	}

	switch e.s("kind") {
	case "run_start":
		// Tie the queue row to the run it turned into.
		//
		// The queue starts an item and the run is minted afterwards, elsewhere,
		// so the row never learned which run it had become — and run_end closes a
		// queue row by run id, which matched nothing. Four rows sat at "running"
		// with no run against them, and with queue.parallel at one that is a
		// queue that will never start anything again. It had been refusing for
		// three days, twenty-six times over.
		//
		// The oldest running row for this ticket that has not been tied to
		// anything: a queue is a line, and the one at the front is the one that
		// just started.
		db.Exec(`UPDATE queue SET run = ? WHERE id = (
		           SELECT id FROM queue WHERE name = ? AND state = 'running'
		             AND ifnull(run,'') = '' ORDER BY id LIMIT 1)`,
			e.s("run"), e.s("name"))
		// A new run of a ticket supersedes any older run still open under the
		// same name. The two share one directory (/runs/<name>), and stop_run
		// is addressed by name: on 28 August a worker restart orphaned a run
		// mid-flight, its open spans aged in this watchdog for two hours, and
		// the blind stop_run they finally earned touched .cancel into the
		// directory the ticket's NEXT run was nine minutes into using — a dead
		// run's verdict killed the live one. A run that was still open when
		// its ticket started over was not going to finish; say so and close it.
		db.Exec(`UPDATE runs SET finished=?, verdict='BLOCKED',
		         summary='superseded: a newer run of this ticket started'
		         WHERE name=? AND run<>? AND finished IS NULL`,
			at, e.s("name"), e.s("run"))
		// REPLACE, not IGNORE: if an earlier event created the row, this is
		// where it learns the ticket's name and its real start.
		db.Exec(`INSERT OR REPLACE INTO runs (run, name, started) VALUES (?,?,?)`,
			e.s("run"), e.s("name"), at)
	case "flow":
		phases, _ := json.Marshal(e["phases"])
		db.Exec(`INSERT OR REPLACE INTO flows (run, at, mode, phases)
		         VALUES (?,?,?,?)`,
			e.s("run"), at, e.s("mode"), string(phases))
	case "run_end":
		db.Exec(`UPDATE runs SET finished=?, verdict=?, summary=? WHERE run=?`,
			at, e.s("verdict"), e.s("summary"), e.s("run"))
		db.Exec(`UPDATE queue SET state='done', finished=? WHERE run=?`, at, e.s("run"))
		// A run that ended having done nothing.
		//
		// Every rule here is about overshoot — too many turns, too long, too
		// much money — and a run that dies a minute in breaks none of them. It
		// spends nothing. It is, on every number this service watches, an
		// exemplary run. One ended on a NameError ninety seconds after it
		// started, wrote its verdict, and nothing anywhere said a word; it was
		// noticed eleven minutes later by somebody watching a turn counter that
		// was never going to move.
		//
		// Undershoot is the other half of the same job. A run is here to do
		// work, and one that finished without taking a turn did not.
		// In a goroutine, because store() holds mu with a defer and both
		// judgeEmptyRun and judge() take it themselves. Called inline, the first
		// run_end after this shipped locked the watchdog and every event behind
		// it: /runs stopped answering, the deploy refused rather than guess, and
		// nothing was judged again until the container was restarted. A mutex in
		// Go is not reentrant and this file locks in a dozen places — anything
		// called from inside store() must take the lock for itself, later.
		go judgeEmptyRun(e.s("run"), e.s("verdict"), e.s("summary"))
	case "gate":
		// The round is counted here rather than trusted from the caller: a
		// pipeline knows what it found, and this service knows how many times it
		// has been told.
		var round int
		db.QueryRow(`SELECT count(*) FROM gates WHERE run=? AND phase=? AND
		             coalesce(branch,'')=?`, e.s("run"), e.s("phase"),
			e.s("branch")).Scan(&round)
		db.Exec(`INSERT INTO gates (at, run, phase, branch, verdict, findings,
		         round, seconds) VALUES (?,?,?,?,?,?,?,?)`,
			at, e.s("run"), e.s("phase"), e.s("branch"), e.s("verdict"),
			int(e.f("findings")), round+1, e.f("seconds"))
	case "call":
		// The counts are worked out here rather than trusted from the caller:
		// the client knows what it just did, this service knows how often it has
		// been told, and only one of those is a count.
		input, _ := e["input"].(map[string]any)
		kind, target, signature := classify(e.s("tool"), input)
		var repeat, onTarget int
		db.QueryRow(`SELECT count(*) FROM calls WHERE run=? AND signature=?`,
			e.s("run"), signature).Scan(&repeat)
		if target != "" {
			db.QueryRow(`SELECT count(*) FROM calls WHERE run=? AND target=?`,
				e.s("run"), target).Scan(&onTarget)
		}
		ok := 1
		if v, is := e["ok"].(bool); is && !v {
			ok = 0
		}
		db.Exec(`INSERT INTO calls (at, run, phase, branch, agent, session, tool,
		         kind, target, signature, repeat, on_target, seconds, ok, why, request)
		         VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			at, e.s("run"), e.s("phase"), e.s("branch"), e.s("agent"),
			e.s("session"), e.s("tool"), kind, target, signature,
			repeat+1, onTarget+1, e.f("seconds"), ok, e.s("why"), e.s("request"))
	case "spend":
		db.Exec(`UPDATE runs SET usd = coalesce(usd,0) + ? WHERE run=?`,
			e.f("usd"), e.s("run"))
	case "turn":
		db.Exec(`UPDATE runs SET turns = coalesce(turns,0) + 1 WHERE run=?`, e.s("run"))
	}
}
