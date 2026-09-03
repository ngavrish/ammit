package main

import "log"

// What an event becomes when it lands: a row in the table that is about it.
//
// gates and calls have always had their own tables, and every chart of them
// is a plain SELECT. turn, suite and heal_lap did not, so a chart of any of
// them was json_extract over the whole event stream, the same expression
// copied into every query, and a chart with a subquery per row that took
// seconds. The event is still kept whole in events; this is the column view
// of it, keyed by the event's own id so nothing is lifted twice.
func lift(id int64, at float64, e event) {
	switch e.s("kind") {
	case "turn":
		// Two provenances, two columns: tokens_out is exactly what the SDK
		// reported, out_est is the runner's measure of the message. They are
		// not folded here - a reader that wants "output" takes the larger in
		// its query and knows it did; a column named tokens_out that is
		// sometimes an estimate is a lie nothing downstream can undo.
		execSQL(`INSERT OR IGNORE INTO turns (event_id, at, run, agent, phase, branch, request, model,
		        context, tokens_in, tokens_out, out_est, cache_read, cache_write, cache_write_1h, geo)
		      VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, at, e.s("run"), e.s("agent"), e.s("phase"), e.s("branch"), e.s("request"), e.s("model"),
			int64(e.f("context")), e.opt("tokens_in"), e.opt("tokens_out"), e.opt("out_est"), e.opt("cache_read"),
			e.opt("cache_write"), e.opt("cache_write_1h"), e.s("geo"))
	case "suite":
		execSQL(`INSERT OR IGNORE INTO suites (event_id, at, run, branch, verdict, total, passed, failed, reason)
		      VALUES (?,?,?,?,?,?,?,?,?)`,
			id, at, e.s("run"), e.s("branch"), e.s("verdict"), e.opt("total"), e.opt("passed"),
			e.opt("failed"), e.s("reason"))
	case "heal_lap":
		execSQL(`INSERT OR IGNORE INTO heal_laps (event_id, at, run, branch, phase, lap, cap, decision)
		      VALUES (?,?,?,?,?,?,?,?)`,
			id, at, e.s("run"), e.s("branch"), e.s("phase"), int64(e.f("lap")), e.opt("cap"),
			e.s("decision"))
		// The runner enforces this one; ammit writes it down beside the limits
		// it enforces itself, so the branch that was given up is in the same
		// table as the run that was stopped.
		if e.s("decision") == "gave_up" {
			judgeLater(e)
		}
	}
}

// opt is a number the event may not carry: NULL when absent, so a column can
// tell "not reported" from zero.
func (e event) opt(key string) any {
	if _, ok := e[key]; !ok {
		return nil
	}
	return int64(e.f(key))
}

func execSQL(sql string, args ...any) {
	if _, err := db.Exec(sql, args...); err != nil {
		log.Printf("ammit: lift: %v", err)
	}
}

// liftHistory lifts every event already kept that has no row yet, once, on
// start. Runs from before the runner posted suite and heal_lap events are
// read from what they left: the branchrun phase's text for the suite, the
// heal phases for the laps. Those rows say so in their reason and decision,
// and they are not re-read once lifted.
func liftHistory() {
	mu.Lock()
	defer mu.Unlock()
	const text = "json_extract(payload,'$.text')"
	for _, sql := range []string{
		`INSERT OR IGNORE INTO turns (event_id, at, run, agent, phase, branch, request, model,
		        context, tokens_in, tokens_out, out_est, cache_read, cache_write, cache_write_1h, geo)
		 SELECT id, at, run, agent, phase, branch, json_extract(payload,'$.request'),
		        json_extract(payload,'$.model'), json_extract(payload,'$.context'),
		        json_extract(payload,'$.tokens_in'),
		        json_extract(payload,'$.tokens_out'), json_extract(payload,'$.out_est'),
		        json_extract(payload,'$.cache_read'), json_extract(payload,'$.cache_write'),
		        json_extract(payload,'$.cache_write_1h'), json_extract(payload,'$.geo')
		 FROM events WHERE kind='turn' AND id > (SELECT coalesce(max(event_id),0) FROM turns)`,
		`INSERT OR IGNORE INTO suites (event_id, at, run, branch, verdict, total, passed, failed, reason)
		 SELECT id, at, run, coalesce(json_extract(payload,'$.branch'),''), json_extract(payload,'$.verdict'),
		        json_extract(payload,'$.total'), json_extract(payload,'$.passed'), json_extract(payload,'$.failed'),
		        coalesce(json_extract(payload,'$.reason'),'')
		 FROM events WHERE kind='suite'`,
		// Before the suite event: the SUITE line of the branchrun phase, taken
		// apart once here and never again. The whole line is the reason.
		`INSERT OR IGNORE INTO suites (event_id, at, run, branch, verdict, total, passed, failed, reason)
		 SELECT id, at, run, coalesce(json_extract(payload,'$.branch'),''),
		        CASE WHEN instr(` + text + `,'SUITE GREEN')>0 THEN 'green' ELSE 'red' END,
		        CAST(substr(substr(` + text + `, instr(` + text + `,'SUITE ')), instr(substr(` + text + `, instr(` + text + `,'SUITE ')),': ')+2) AS INTEGER),
		        CAST(substr(` + text + `, instr(` + text + `,'scenarios, ')+11) AS INTEGER),
		        CAST(substr(` + text + `, instr(` + text + `,'passed, ')+8) AS INTEGER),
		        'from the phase text: ' || substr(substr(` + text + `, instr(` + text + `,'SUITE ')), 1, 120)
		 FROM events WHERE kind='phase_end' AND json_extract(payload,'$.phase')='branchrun'
		   AND run NOT IN (SELECT run FROM events WHERE kind='suite')`,
		`INSERT OR IGNORE INTO heal_laps (event_id, at, run, branch, phase, lap, cap, decision)
		 SELECT id, at, run, coalesce(json_extract(payload,'$.branch'),''), phase,
		        json_extract(payload,'$.lap'), json_extract(payload,'$.cap'), json_extract(payload,'$.decision')
		 FROM events WHERE kind='heal_lap'`,
		// Before the heal_lap event: one lap per heal phase that ended, numbered
		// in order within its branch; the cap was not recorded, the decision
		// is not known.
		`INSERT OR IGNORE INTO heal_laps (event_id, at, run, branch, phase, lap, cap, decision)
		 SELECT id, at, run, branch, phase,
		        row_number() OVER (PARTITION BY run, branch ORDER BY at), NULL, 'unknown'
		 FROM (SELECT id, at, run, coalesce(json_extract(payload,'$.branch'),'') AS branch, phase
		       FROM events WHERE kind='phase_end' AND json_extract(payload,'$.phase') LIKE '%heal%'
		         AND run NOT IN (SELECT run FROM events WHERE kind='heal_lap'))`,
	} {
		execSQL(sql)
	}
}

// judgeLater records a heal loop given up, outside the lock store holds.
func judgeLater(e event) {
	run, branch, cap, lap := e.s("run"), e.s("branch"), e.f("cap"), e.f("lap")
	go func() {
		var name string
		mu.Lock()
		db.QueryRow(`SELECT coalesce(name,'') FROM runs WHERE run=?`, run).Scan(&name)
		mu.Unlock()
		judge("branch", run, name+" "+branch, "loops.heal_laps_per_branch", cap, lap, "none",
			"the runner gave the branch up, unconverged")
	}()
}
