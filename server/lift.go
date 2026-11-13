package main

import (
	"log"
	"strings"
)

// What an event becomes when it lands: a row in the table that is about it.
//
// gates and calls have always had their own tables, and every chart of them
// is a plain SELECT. turn, suite and heal_lap did not, so a chart of any of
// them was json_extract over the whole event stream, the same expression
// copied into every query, and a chart with a subquery per row that took
// seconds. The event is still kept whole in events; this is the column view
// of it, keyed by the event's own id so nothing is lifted twice.
func lift(id int64, at float64, e event) {
	liftAnswer(id, at, e)
	switch e.s("kind") {
	case "turn":
		// Two provenances, two columns: tokens_out is exactly what the SDK
		// reported, out_est is the runner's measure of the message. They are
		// not folded here - a reader that wants "output" takes the larger in
		// its query and knows it did; a column named tokens_out that is
		// sometimes an estimate is a lie nothing downstream can undo.
		execSQL(`INSERT INTO turns (event_id, at, run, agent, phase, branch, request, model,
		        context, tokens_in, tokens_out, out_est, cache_read, cache_write, cache_write_1h, geo)
		      VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
			id, at, e.s("run"), e.s("agent"), e.s("phase"), e.s("branch"), e.s("request"), e.s("model"),
			int64(e.f("context")), e.opt("tokens_in"), e.opt("tokens_out"), e.opt("out_est"), e.opt("cache_read"),
			e.opt("cache_write"), e.opt("cache_write_1h"), e.s("geo"))
	case "suite":
		execSQL(`INSERT INTO suites (event_id, at, run, branch, verdict, total, passed, failed, reason)
		      VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
			id, at, e.s("run"), e.s("branch"), e.s("verdict"), e.opt("total"), e.opt("passed"),
			e.opt("failed"), e.s("reason"))
	case "result":
		liftResults(id, at, e)
	case "heal_lap":
		execSQL(`INSERT INTO heal_laps (event_id, at, run, branch, phase, lap, cap, decision)
		      VALUES (?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
			id, at, e.s("run"), e.s("branch"), e.s("phase"), int64(e.f("lap")), e.opt("cap"),
			e.s("decision"))
		// The runner enforces this one; ammit writes it down beside the limits
		// it enforces itself, so the branch that was given up is in the same
		// table as the run that was stopped.
		if e.s("decision") == "gave_up" {
			judgeLater(e)
		}
	case "limit":
		judgeRunnerLimit(e)
	}
}

// liftResults makes one results row per scenario of a result event.
func liftResults(id int64, at float64, e event) {
	rows, _ := e["rows"].([]any)
	for i, raw := range rows {
		r, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		row := event(r)
		execSQL(`INSERT INTO results (event_id, idx, at, run, ticket, phase, branch, feature, suite,
		        file, name, classname, status, message, text, seconds, product_commit, observed_at)
		      VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
			id, int64(i), at, e.s("run"), e.s("ticket"), e.s("phase"), e.s("branch"), e.s("feature"),
			e.s("suite"), e.s("file"), row.s("name"), row.s("classname"), row.s("status"), row.s("message"), row.s("text"),
			row.optf("seconds"), e.s("commit"), e.optf("observed_at"))
	}
}

// optf is a real number the event may not carry: NULL when absent.
func (e event) optf(key string) any {
	if _, ok := e[key]; !ok {
		return nil
	}
	return e.f(key)
}

// judgeRunnerLimit writes down a limit the runner applied itself.
//
// The runner holds no numbers of its own any more: each one is a key in
// limits.yml, and every time one bites the runner says which, against what,
// and what it did. Until 9 October the rule-repair cap was enforced, and
// bypassed, inside the runner with nothing here to count it - claim-9 of
// APF-3296 went five laps on a cap of two. Recorded as a judgement with
// scope "runner", so it reads beside the limits this service enforces, and
// outside the lock store holds, as judgeLater does.
func judgeRunnerLimit(e event) {
	run, rule, action := e.s("run"), e.s("rule"), e.s("action")
	threshold, observed := e.f("threshold"), e.f("observed")
	subject := strings.TrimSpace(strings.Join([]string{e.s("phase"), e.s("branch"), e.s("agent")}, " "))
	go judge("runner", run, subject, rule, threshold, observed, action, "applied by the runner")
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
	// The history is replayed with the same reading of a payload the live
	// path makes: jnum for what the event sent as a number (json_extract's
	// number, see schema.go), ->> for text, lead_int for SQLite's CAST of a
	// string to INTEGER - the leading digits, 0 when there are none.
	const text = "(payload->>'text')"
	for _, sql := range []string{
		`INSERT INTO turns (event_id, at, run, agent, phase, branch, request, model,
		        context, tokens_in, tokens_out, out_est, cache_read, cache_write, cache_write_1h, geo)
		 SELECT id, at, run, agent, phase, branch, payload->>'request',
		        payload->>'model', trunc(jnum(payload->'context')),
		        trunc(jnum(payload->'tokens_in')),
		        trunc(jnum(payload->'tokens_out')), trunc(jnum(payload->'out_est')),
		        trunc(jnum(payload->'cache_read')), trunc(jnum(payload->'cache_write')),
		        trunc(jnum(payload->'cache_write_1h')), payload->>'geo'
		 FROM events WHERE kind='turn' AND id > (SELECT coalesce(max(event_id),0) FROM turns)
		 ON CONFLICT DO NOTHING`,
		`INSERT INTO suites (event_id, at, run, branch, verdict, total, passed, failed, reason)
		 SELECT id, at, run, coalesce(payload->>'branch',''), payload->>'verdict',
		        trunc(jnum(payload->'total')), trunc(jnum(payload->'passed')), trunc(jnum(payload->'failed')),
		        coalesce(payload->>'reason','')
		 FROM events WHERE kind='suite'
		 ON CONFLICT DO NOTHING`,
		// Before the suite event: the SUITE line of the branchrun phase, taken
		// apart once here and never again. The whole line is the reason.
		`INSERT INTO suites (event_id, at, run, branch, verdict, total, passed, failed, reason)
		 SELECT id, at, run, coalesce(payload->>'branch',''),
		        CASE WHEN strpos(` + text + `,'SUITE GREEN')>0 THEN 'green' ELSE 'red' END,
		        lead_int(substr(substr(` + text + `, strpos(` + text + `,'SUITE ')), strpos(substr(` + text + `, strpos(` + text + `,'SUITE ')),': ')+2)),
		        lead_int(substr(` + text + `, strpos(` + text + `,'scenarios, ')+11)),
		        lead_int(substr(` + text + `, strpos(` + text + `,'passed, ')+8)),
		        'from the phase text: ' || substr(substr(` + text + `, strpos(` + text + `,'SUITE ')), 1, 120)
		 FROM events WHERE kind='phase_end' AND payload->>'phase'='branchrun'
		   AND run NOT IN (SELECT run FROM events WHERE kind='suite')
		 ON CONFLICT DO NOTHING`,
		// A result event whose rows were not lifted when it landed (the lift
		// is best-effort): every row, once.
		`INSERT INTO results (event_id, idx, at, run, ticket, phase, branch, feature, suite,
		        file, name, classname, status, message, text, seconds, product_commit, observed_at)
		 SELECT e.id, r.ord - 1, e.at, e.run, coalesce(e.payload->>'ticket',''), coalesce(e.phase,''),
		        coalesce(e.branch,''), coalesce(e.payload->>'feature',''), coalesce(e.payload->>'suite',''),
		        coalesce(e.payload->>'file',''), coalesce(r.v->>'name',''), coalesce(r.v->>'classname',''),
		        coalesce(r.v->>'status',''),
		        coalesce(r.v->>'message',''), coalesce(r.v->>'text',''), jnum(r.v->'seconds'),
		        coalesce(e.payload->>'commit',''),
		        jnum(e.payload->'observed_at')
		 FROM events e, jsonb_array_elements(e.payload->'rows') WITH ORDINALITY AS r(v, ord)
		 WHERE e.kind='result' AND jsonb_typeof(e.payload->'rows') = 'array'
		 ON CONFLICT DO NOTHING`,
		`INSERT INTO heal_laps (event_id, at, run, branch, phase, lap, cap, decision)
		 SELECT id, at, run, coalesce(payload->>'branch',''), phase,
		        trunc(jnum(payload->'lap')), trunc(jnum(payload->'cap')), payload->>'decision'
		 FROM events WHERE kind='heal_lap'
		 ON CONFLICT DO NOTHING`,
		// Before the heal_lap event: one lap per heal phase that ended, numbered
		// in order within its branch; the cap was not recorded, the decision
		// is not known.
		`INSERT INTO heal_laps (event_id, at, run, branch, phase, lap, cap, decision)
		 SELECT id, at, run, branch, phase,
		        row_number() OVER (PARTITION BY run, branch ORDER BY at), NULL, 'unknown'
		 FROM (SELECT id, at, run, coalesce(payload->>'branch','') AS branch, phase
		       FROM events WHERE kind='phase_end' AND payload->>'phase' ILIKE '%heal%'
		         AND run NOT IN (SELECT run FROM events WHERE kind='heal_lap')) AS h
		 ON CONFLICT DO NOTHING`,
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
		_, rule := healCap(loadConfig(env("AMMIT_CONFIG", "/config/limits.yml")))
		judge("branch", run, name+" "+branch, rule, cap, lap, "none",
			"the runner gave the branch up, unconverged")
	}()
}
