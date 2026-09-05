package main

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// What a run's spans say: what is open, what is quiet, what is stuck.

// closeSpansOf writes the closing events for everything still open under a run
// that is over.
//
// A run does not end tidily. It is stopped from outside, or its worker is
// restarted under it, and whatever was in flight at that moment — a request, a
// session, a phase, a deploy — never sends the event that would close it. Left
// alone those spans read as still running, for ever: the charts draw them
// growing, and anything asking "what is open" gets an answer about a run that
// finished last night. Two of them have been sitting in this database since a
// three-second restart yesterday.
//
// A span cannot outlive its run. So the run's end is their end, recorded as
// what it is — closed because the run ended, not because the work finished —
// and never mistaken for a clean one: ok is false and the reason says who did
// it.
// notClosed drops the spans ammit closed on a run's behalf, unless asked for.
// They carry an age where a duration belongs and it is measured in hours.
func notClosed(r *http.Request) string {
	if r.URL.Query().Get("closed") != "" {
		return ""
	}
	return " AND ifnull(json_extract(payload,'$.error'),'') NOT LIKE 'closed by ammit%'"
}

func closeSpansOf(run string, at float64, why string) {
	pairs := []struct{ start, end, column string }{
		{"request_start", "request_end", "session"},
		{"item_start", "item_end", "session"},
		// Keyed on the session, not on the agent: a fan-out runs seven
		// branches of one agent at once, and under the agent's name they are a
		// single span opening seven times. The client sends agent@branch.
		{"session_start", "session_end", "session"},
		{"phase_start", "phase_end", "phase"},
	}
	for _, p := range pairs {
		for name, age := range openSpans(run, p.start, p.end, p.column) {
			e := event{"kind": p.end, "at": at, "run": run, p.column: name,
				"ok": false, "seconds": age,
				"error": "closed by ammit: " + why}
			if p.column != "session" {
				e["session"] = name
			}
			store(e)
		}
	}
}

type openRun struct {
	run, name    string
	started, usd float64
	turns        int
}

func openRuns() []openRun {
	mu.Lock()
	defer mu.Unlock()
	rows, err := db.Query(`SELECT run, coalesce(name,''), started, coalesce(usd,0),
	                       coalesce(turns,0) FROM runs
	                       WHERE finished IS NULL AND started > ?`,
		float64(time.Now().Add(-72*time.Hour).UnixNano())/1e9)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []openRun
	for rows.Next() {
		var r openRun
		if err := rows.Scan(&r.run, &r.name, &r.started, &r.usd, &r.turns); err == nil {
			out = append(out, r)
		}
	}
	return out
}

// quietFor is how long a run has said nothing.
//
// The client reports a turn per exchange, so silence inside a live run is a turn
// that has not come back — which is the failure this whole service was written
// for, and the one nothing inside the pipeline could see.
// lastBranch is the branch of the newest event of this run, or "" if this run
// never fanned out.
func lastBranch(run string) string {
	mu.Lock()
	defer mu.Unlock()
	var branch string
	db.QueryRow(`SELECT coalesce(branch,'') FROM events WHERE run=? AND ifnull(branch,'') <> ''
	             ORDER BY id DESC LIMIT 1`, run).Scan(&branch)
	return branch
}

// afterTool says whether this wait is one the agent caused by asking for a
// tool. The session logs the tool call, then waits for it to finish, so the
// newest thing said by this session before the wait began settles it.
func afterTool(run, request string) bool {
	mu.Lock()
	defer mu.Unlock()
	var started float64
	var agent, wait string
	if err := db.QueryRow(`SELECT at, coalesce(agent,''),
	                       coalesce(json_extract(payload,'$.wait'),'')
	                       FROM events
	                       WHERE run=? AND kind='request_start' AND session=?
	                       ORDER BY id DESC LIMIT 1`, run, request).
		Scan(&started, &agent, &wait); err != nil {
		return false
	}
	// The runner says what the wait covers, when it says anything: "tool"
	// spans a tool's execution and gets the browser-suite hour, "model" means
	// the tools are settled and only the answer is outstanding - which has no
	// business taking ten minutes. The log-level guess below misread exactly
	// that case: the runner logs a tool's RESULT at level tool too, so a hung
	// model stream right after a delivered result sat under the hour ceiling
	// for thirteen minutes on run 0337ed5e.
	if wait == "tool" || wait == "model" {
		return wait == "tool"
	}
	var level string
	if err := db.QueryRow(`SELECT coalesce(json_extract(payload,'$.level'),'')
	                       FROM events WHERE run=? AND kind='log' AND agent=? AND at<=?
	                       ORDER BY id DESC LIMIT 1`, run, agent, started).
		Scan(&level); err != nil {
		return false
	}
	return level == "tool"
}

// spanFacts is who opened this wait, on which branch, and when.
func spanFacts(run, request string) (string, string, float64) {
	mu.Lock()
	defer mu.Unlock()
	var at float64
	var agent, branch string
	db.QueryRow(`SELECT at, coalesce(agent,''), coalesce(branch,'')
	             FROM events WHERE run=? AND kind='request_start' AND session=?
	             ORDER BY id DESC LIMIT 1`, run, request).Scan(&at, &agent, &branch)
	return agent, branch, at
}

// spanIsOrphaned says whether this request belongs to a session that has since
// ended — which makes it a lost event rather than a hanging call.
func spanIsOrphaned(run, request string) bool {
	agent, branch, at := spanFacts(run, request)
	if agent == "" {
		return false
	}
	mu.Lock()
	defer mu.Unlock()
	var ended int
	db.QueryRow(`SELECT count(*) FROM events WHERE run=? AND kind='session_end'
	             AND coalesce(agent,'')=? AND coalesce(branch,'')=? AND at>=?`,
		run, agent, branch, at).Scan(&ended)
	return ended > 0
}

// itemFacts is what an open unit of work said about itself when it started:
// what kind of thing it is, and who is running it.
func itemFacts(run, item string) (string, string) {
	mu.Lock()
	defer mu.Unlock()
	var kind, agent string
	// itemkind, not kind: `kind` on an event is already the event's own name, so
	// what sort of unit this is has to travel under a name of its own.
	db.QueryRow(`SELECT coalesce(json_extract(payload,'$.itemkind'),''), coalesce(agent,'')
	             FROM events WHERE run=? AND kind='item_start' AND session=?
	             ORDER BY id DESC LIMIT 1`, run, item).Scan(&kind, &agent)
	return kind, agent
}

// turnsPerSession is how many turns each agent has taken in the session it has
// open now — not across the run, which is a different question with a different
// answer when an agent runs once per branch.
func turnsPerSession(run string) map[string]float64 {
	mu.Lock()
	defer mu.Unlock()
	rows, err := db.Query(`
		SELECT coalesce(t.agent,''), count(*) FROM events t
		WHERE t.run=? AND t.kind='turn' AND ifnull(t.agent,'') <> ''
		  AND t.at >= coalesce((SELECT max(s.at) FROM events s
		      WHERE s.run=t.run AND s.kind='session_start'
		        AND coalesce(s.agent,'')=coalesce(t.agent,'')), 0)
		GROUP BY 1`, run)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var who string
		var n float64
		if err := rows.Scan(&who, &n); err == nil {
			out[who] = n
		}
	}
	return out
}

// spinningSessions is every open session that keeps asking the model and has
// not touched a tool for a whole window: at least minRequests request_starts
// in the trailing window seconds and zero tool calls in the same window.
//
// This is the shape a turn count cannot see. The session that motivated it
// made ninety-seven requests in ten minutes, seventy-two returning nothing,
// with no tool call and no error anywhere — formally nothing was wrong, so no
// watchdog reacted, and the phase waited on it while thirteen sibling branches
// had finished. The two signals together are what tells it from honest work:
// a session composing one long answer makes a handful of requests, and a
// session grinding through a suite makes calls — measured across a whole
// four-hour run, no healthy session ever crossed both lines at once.
//
// Returns session key -> requests in the window.
func spinningSessions(run string, window, minRequests float64) map[string]float64 {
	mu.Lock()
	defer mu.Unlock()
	since := float64(time.Now().UnixNano())/1e9 - window
	rows, err := db.Query(`
		SELECT s.session,
		  (SELECT count(*) FROM events r WHERE r.run=s.run AND r.kind='request_start'
		     AND coalesce(r.agent,'')=s.agent AND coalesce(r.branch,'')=s.branch
		     AND r.at>=?) reqs,
		  (SELECT count(*) FROM events c WHERE c.run=s.run AND c.kind='call'
		     AND coalesce(c.session,'')=s.session AND c.at>=?) calls
		FROM (SELECT run, coalesce(session,'') session, coalesce(agent,'') agent,
		             coalesce(branch,'') branch
		      FROM events WHERE run=? AND kind='session_start'
		        AND coalesce(session,'')<>''
		        AND coalesce(session,'') NOT IN (
		            SELECT coalesce(session,'') FROM events
		            WHERE run=? AND kind='session_end')
		      GROUP BY 2) s`, since, since, run, run)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var session string
		var reqs, calls float64
		if err := rows.Scan(&session, &reqs, &calls); err == nil &&
			calls == 0 && reqs >= minRequests {
			out[session] = reqs
		}
	}
	return out
}

// heaviestTurn is the largest single prompt this run has sent recently, and who
// sent it. Recently rather than ever, because a run is judged on what it is
// doing now: one heavy turn an hour ago is history, and a limit that keeps
// firing on history cannot be acted on.
func heaviestTurn(run string) (float64, string, string) {
	mu.Lock()
	defer mu.Unlock()
	since := float64(time.Now().Add(-15*time.Minute).UnixNano()) / 1e9
	var size float64
	var who, phase string
	db.QueryRow(`SELECT coalesce(json_extract(payload,'$.context'),0),
	                    coalesce(agent,'?'), coalesce(phase,'')
	             FROM events WHERE run=? AND kind='turn' AND at>=?
	             ORDER BY coalesce(json_extract(payload,'$.context'),0) DESC
	             LIMIT 1`, run, since).Scan(&size, &who, &phase)
	return size, who, phase
}

// workerBusy asks the machine, not the run: is the worker container visibly
// burning CPU right now? A branch suite is legally silent for ten minutes -
// one behave process, browsers rendering, nothing to report - and the
// run-silence judgement read that honest quiet as a hang. The watched must
// not testify about its own pulse (a self-reported heartbeat is the same
// lie in the other direction); the watchdog's own sampler already measures
// the container every minute, so silence plus a busy machine is work, and
// silence plus an idle machine is the hang this timeout exists for.
func workerBusy(conf Config) bool {
	floor, ok := conf.num("limits", "quiet_cpu")
	if !ok || floor <= 0 {
		return false
	}
	worker := conf.str("context", "worker", "")
	if worker == "" {
		return false
	}
	mu.Lock()
	defer mu.Unlock()
	var at, cpu float64
	err := db.QueryRow(`SELECT at, coalesce(json_extract(payload,'$.cpu_pct'),0)
	                    FROM events WHERE kind='sample'
	                      AND json_extract(payload,'$.container')=?
	                    ORDER BY id DESC LIMIT 1`, worker).Scan(&at, &cpu)
	if err != nil {
		return false
	}
	if float64(time.Now().UnixNano())/1e9-at > 180 {
		return false
	}
	return cpu >= floor
}

// workerUp asks the docker daemon whether the worker container is running.
// Best effort with a short leash: a daemon that cannot answer in five seconds
// is treated as "unknown", and unknown keeps the certificate unsigned - the
// caller then leaves the row open, which is the recoverable mistake.
func workerUp(conf Config) bool {
	worker := conf.str("context", "worker", "")
	if worker == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f",
		"{{.State.Running}}", worker).Output()
	if err != nil {
		return true // unknown: do not certify a death the daemon cannot confirm
	}
	return strings.TrimSpace(string(out)) == "true"
}

// heardFrom is seconds since the WORKER said anything at all - any event it
// sent, agented or not; only the watchdog's own instruments are excluded.
func heardFrom(run string) float64 {
	mu.Lock()
	defer mu.Unlock()
	var at float64
	// heartbeat excluded alongside the watchdog's own instruments: it is the
	// process pulse, and a 60-second pulse would otherwise reset this
	// work-silence measure forever, hiding a wedged session behind a living
	// loop. Work-silence and process-pulse are two questions with two checks.
	err := db.QueryRow(`SELECT at FROM events
	                    WHERE run=? AND kind NOT IN ('sample','netprobe','heartbeat')
	                    ORDER BY id DESC LIMIT 1`, run).Scan(&at)
	if err != nil {
		return 0
	}
	return float64(time.Now().UnixNano())/1e9 - at
}

// heartbeatAge is seconds since the runner PROCESS last said it is looping,
// or -1 when it has never sent a heartbeat (an old run, or one that died
// before the first pulse - do not judge a pulse that never started). This is
// the fast reaper run 14 lacked: the worker was recreated and no work event
// came, but nothing watched the pulse because the pulse was not an event.
func heartbeatAge(run string) float64 {
	mu.Lock()
	defer mu.Unlock()
	var at float64
	err := db.QueryRow(`SELECT at FROM events
	                    WHERE run=? AND kind='heartbeat'
	                    ORDER BY id DESC LIMIT 1`, run).Scan(&at)
	if err != nil {
		return -1
	}
	return float64(time.Now().UnixNano())/1e9 - at
}

func quietFor(run string) (float64, string, string) {
	mu.Lock()
	defer mu.Unlock()
	var at float64
	var agent, phase string
	// The pulse reads only what an AGENT said. Excluding the watchdog's own
	// instruments (samples, probes) was round one; round two was a service_log
	// line - the control's own "retry -> 0 waits" bookkeeping - becoming the
	// run's newest row, so ten minutes later the silence was judged with an
	// empty agent and the BLIND branch shot the run (8c7c591f, minute 161).
	// Any agentless row can only ever blind the aim or fake the pulse, so
	// none of them count: silence is measured from the last thing a session
	// actually said, and the aim lands on that session. A run with no agented
	// events yet has no pulse to judge - better no judgement than a blind one.
	err := db.QueryRow(`SELECT at, coalesce(agent,''), coalesce(phase,'') FROM events
	                    WHERE run=? AND coalesce(agent,'') != ''
	                    ORDER BY id DESC LIMIT 1`, run).Scan(&at, &agent, &phase)
	if err != nil {
		return 0, "", ""
	}
	return float64(time.Now().UnixNano())/1e9 - at, agent, phase
}

// openSpans is what this run has begun and not yet finished, by name.
//
// Asked of the database rather than replayed in here. Replaying meant reading
// every request this run had ever made — fifteen thousand rows, every tick, to
// find the two that were still open — and doing it again for phases and again
// for sessions. The answer was always "the ones with no end", which is a
// sentence SQL can say by itself.
// openPhases is the phase this run is in and how long it has been in it.
//
// Derived, not reported. Every event already carries the phase it happened in —
// that is what the charts are coloured by — so a phase begins at its first event
// and ends at its phase_end, and asking the pipeline to send a phase_start as
// well would be asking it to repeat something it has already said 15,000 times.
// The pair-of-events version never worked precisely because nothing sent the
// first half, and timeouts.phase had quietly never fired.
func openPhases(run string) map[string]float64 {
	mu.Lock()
	defer mu.Unlock()
	// Only a phase somebody OPENED counts as open. Any event carries a phase
	// column — an orchestrator's deploy step logs under phase "envdeploy"
	// without ever starting a phase of that name — and counting those held a
	// phantom phase open for a whole run: the deploy finished in fourteen
	// seconds, the "phase" aged past its limit twice, and stop_phase went out
	// against a phase no worker had, twice, as a no-op.
	rows, err := db.Query(`
		SELECT coalesce(phase,''), min(at) FROM events
		WHERE run=? AND kind='phase_start' AND ifnull(phase,'') <> ''
		  AND coalesce(phase,'') NOT IN (
		      SELECT coalesce(phase,'') FROM events WHERE run=? AND kind='phase_end')
		GROUP BY 1`, run, run)
	if err != nil {
		return nil
	}
	defer rows.Close()
	now := float64(time.Now().UnixNano()) / 1e9
	live := map[string]float64{}
	for rows.Next() {
		var phase string
		var at float64
		if err := rows.Scan(&phase, &at); err == nil {
			live[phase] = now - at
		}
	}
	return live
}

// sessionQuiet is how long a session has said nothing: seconds since its last
// event of any kind - the session key on a call, or the agent and branch
// columns a request or a log line carries. Returns -1 when nothing matches,
// so the caller can fall back to the span's age rather than judging silence
// nobody measured.
func sessionQuiet(run, session string) float64 {
	agent, branch := session, ""
	if at := strings.Index(session, "@"); at >= 0 {
		agent, branch = session[:at], session[at+1:]
	}
	mu.Lock()
	defer mu.Unlock()
	var last float64
	err := db.QueryRow(`SELECT max(at) FROM events
	                    WHERE run=? AND (session=?
	                       OR (coalesce(agent,'')=? AND coalesce(branch,'')=?))`,
		run, session, agent, branch).Scan(&last)
	if err != nil || last == 0 {
		return -1
	}
	return float64(time.Now().UnixNano())/1e9 - last
}

func openSpans(run, startKind, endKind, column string) map[string]float64 {
	mu.Lock()
	defer mu.Unlock()
	rows, err := db.Query(fmt.Sprintf(`
		SELECT coalesce(%[1]s,''), max(at) FROM events
		WHERE run=? AND kind=? AND ifnull(%[1]s,'') <> ''
		  AND coalesce(%[1]s,'') NOT IN (
		      SELECT coalesce(%[1]s,'') FROM events WHERE run=? AND kind=?)
		GROUP BY 1`, column), run, startKind, run, endKind)
	if err != nil {
		return nil
	}
	defer rows.Close()
	now := float64(time.Now().UnixNano()) / 1e9
	live := map[string]float64{}
	for rows.Next() {
		var key string
		var at float64
		if err := rows.Scan(&key, &at); err == nil {
			live[key] = now - at
		}
	}
	return live
}

// retryWaits: seconds each open session of a run has spent, inside the last
// `window` seconds, waiting behind the CLI's own API retries - request_end
// rows whose detail names api_retry. The wait is the back-off before the
// retry the row announces, which the runner records as the time the message
// took to arrive. Sessions with nothing to say are absent.
func retryWaits(run string, window float64) map[string]float64 {
	mu.Lock()
	defer mu.Unlock()
	since := float64(time.Now().UnixNano())/1e9 - window
	rows, err := db.Query(`
		SELECT coalesce(agent,'') || CASE WHEN coalesce(branch,'')<>'' THEN '@'||branch ELSE '' END,
		       sum(coalesce(json_extract(payload,'$.seconds'),0))
		FROM events
		WHERE run=? AND kind='request_end' AND at>=?
		  AND coalesce(json_extract(payload,'$.detail'),'') LIKE '%api_retry%'
		GROUP BY 1`, run, since)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var session string
		var secs float64
		if rows.Scan(&session, &secs) == nil && session != "" {
			out[session] = secs
		}
	}
	return out
}
