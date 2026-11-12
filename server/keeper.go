package main

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
)

// keepTick is the plain watchman: every watchdog.keep_every seconds, for each
// ticket in watchdog.keep ("all": every ticket run in the last two days), if nothing of the ticket is running or queued and
// its last run did not end PASS or FAIL, queue a resume from where that run
// stopped. No fix cycles and no cap - the watchdog's cycles fix code; this only
// sees to it that a ticket someone is waiting on is never left lying.
//
// 2026-10-27: APF-3239 sat BLOCKED on the host stand from 08:59 to 09:22 and
// earlier for longer, the watchdog skipping host runs and APF-3301's chain out
// of its three cycles, until a person looked. A ticket on the list is asked
// after every quarter of an hour.
func keepTick(conf Config) {
	tickets := conf.str("watchdog", "keep", "")
	if strings.TrimSpace(tickets) == "" || handsOff(conf) {
		return
	}
	every, ok := conf.num("watchdog", "keep_every")
	if !ok || every <= 0 {
		every = 900
	}
	if last, known := wdMeta("keep_at"); known && wdNow()-last < every {
		return
	}
	wdSetMeta("keep_at", wdNow())
	list := strings.Split(tickets, ",")
	if strings.TrimSpace(tickets) == "all" {
		list = recentTickets()
	}
	for _, t := range list {
		if t = strings.TrimSpace(t); t != "" {
			keepOne(t, conf)
		}
	}
}

// recentTickets is every ticket with a run started in the last two days:
// watchdog.keep "all" watches each run without anyone listing its ticket.
// Ad-hoc questions are not tickets: on 2026-11-06 the keeper resumed two of
// them every tick into "no such ticket" and "no such flow".
// 2026-11-05: keep was empty, so the keeper watched nothing, and APF-1972
// lay stopped while a person asked in a chat why nothing picked it up.
func recentTickets() []string {
	mu.Lock()
	defer mu.Unlock()
	rows, err := db.Query(`SELECT DISTINCT name FROM runs WHERE started > ?
	                       AND name ~ '^[A-Z].*-[0-9]' AND name NOT ILIKE 'WD-%'
	                       AND name NOT ILIKE 'ADHOC-%'`, wdNow()-2*86400)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			out = append(out, n)
		}
	}
	_ = rows.Err() // a cut-off read is the shorter list it always was
	return out
}

func keepOne(ticket string, conf Config) {
	mu.Lock()
	var busy int
	db.QueryRow(`SELECT (SELECT count(*) FROM runs WHERE name=? AND finished IS NULL)
	                  + (SELECT count(*) FROM queue WHERE name=? AND state IN ('waiting','running'))`,
		ticket, ticket).Scan(&busy)
	var run, verdict, summary string
	db.QueryRow(`SELECT run, coalesce(verdict,''), coalesce(summary,'') FROM runs
	             WHERE name=? AND finished IS NOT NULL ORDER BY started DESC LIMIT 1`,
		ticket).Scan(&run, &verdict, &summary)
	mu.Unlock()
	if busy > 0 || run == "" {
		return
	}
	// Every account spent: wait for the hour the runner named, not restart
	// into the same refusal every quarter of an hour.
	if poolBack(run) > wdNow() {
		return
	}
	switch {
	case verdict == "PASS" || verdict == "FAIL" || verdict == "PARTIAL":
		return
	case strings.Contains(summary, "waits for a person's approval"):
		return
	}
	f, err := readRunFacts(run)
	if err != nil {
		return
	}
	// A run a person stopped is not lying: it was put down. 2026-10-27: the
	// keeper resumed APF-3301's 13c9a745, stopped by hand a minute earlier
	// because a later run had superseded it.
	// A stop through the host orchestrator's own /cancel is a person's too,
	// though no /act judgement says so (stoppedByHand). 2026-11-06: this
	// queued 3f7ba7eb twenty seconds after e4420a99 of APF-1972 was put down
	// that way, and the same hand put the resume down two minutes later.
	if _, by := stoppedByHand(f); by {
		return
	}
	// Over budget is not a reason to leave a ticket lying: the keeper resumes
	// it like any other stop (2026-11-12, the person's rule: watchdogs are
	// unlimited). APF-3313 lay from 18:29 with its budget spent and nobody told.
	// A held ticket is its holder's, whatever the keeper would do with it.
	mu.Lock()
	holder := openActionOf(db, ticket, false)
	mu.Unlock()
	if holder != nil {
		sayHeldOnce("keeper", ticket, holder)
		return
	}
	phase := resumeFrom(f)
	if handToFixer(ticket, f, phase, conf) {
		return
	}
	body := map[string]any{"key": ticket, "rerun": "resume", "source_run": run}
	if f.mode != "" {
		body["mode"] = f.mode
	}
	if phase != "" {
		body["from_phase"] = phase
	}
	for k, v := range runParams(run) {
		body[k] = v
	}
	payload, _ := json.Marshal(body)
	// Through ticket_actions (actions.go): a ticket a watchdog cycle is
	// fixing, a stop is closing or another resume is queued for is held, and
	// the keeper leaves it to its holder. 2026-11-07: the keeper resumed
	// APF-3313 a minute into cycle 3's fix run WD-9519cad8-3, the resume hit
	// the same red gate, and the cycle was spent before its PR was merged.
	q, holder := queueUnderAction(ticket, "keeper", run, string(payload))
	if q == 0 {
		sayHeldOnce("keeper", ticket, holder)
		return
	}
	text := fmt.Sprintf("keeper: %s was lying since run %s ended %s; resume queued from %s (queue %d)",
		ticket, shortRun(run), orDash(verdict), orDash(phase), q)
	store(event{"kind": "keeper", "run": run, "text": text})
	log.Printf("ammit: %s", text)
}

// What a BLOCKED run is compared on: its stored summary with run ids, numbers
// and spacing taken out, and the phase a resume of it would start from. "gate
// funcreqrules red on 3 branch(es) in 4ef880b4" and the same line a run later
// with 5 branches and another id are one blocker.
var blockedNoise = regexp.MustCompile(`\b[0-9a-f]{8,}\b|[0-9]+(\.[0-9]+)?`)

func blockedKey(summary, phase string) string {
	s := blockedNoise.ReplaceAllString(strings.ToLower(summary), "#")
	return strings.Join(strings.Fields(s), " ") + " @ " + orDash(phase)
}

// handToFixer hands a ticket to the fixer instead of resuming it again, and
// says whether the keeper must leave it: the run ended BLOCKED the same way
// (blockedKey) as the ticket's run before it. A first BLOCKED is resumed as it
// always was; the second in a row is not something a resume changes. The
// hand-over is the watchdog's own entry - the drop fact, recordDrop,
// startCycle - so it is a cycle like any other: its fix action on the ticket,
// the fix run with the evidence, PR, merge, deploy, resume, counted against
// loops.laps_watchdog on the run's chain. Once per run: a ticket handed over
// is the cycle's, or with the cycles spent a person's.
func handToFixer(ticket string, f *runFacts, phase string, conf Config) bool {
	if f.verdict != "BLOCKED" {
		return false
	}
	var prev, verdict string
	mu.Lock()
	db.QueryRow(`SELECT run, coalesce(verdict,'') FROM runs
	             WHERE name=? AND finished IS NOT NULL AND run<>? AND started < ?
	             ORDER BY started DESC LIMIT 1`, ticket, f.run, f.started).Scan(&prev, &verdict)
	mu.Unlock()
	if prev == "" || strings.ToUpper(verdict) != "BLOCKED" {
		return false
	}
	pf, err := readRunFacts(prev)
	if err != nil {
		return false
	}
	key := blockedKey(f.summary, phase)
	if key != blockedKey(pf.summary, resumeFrom(pf)) {
		return false
	}
	if _, done := wdMeta("escalated:" + f.run); done {
		return true
	}
	d, why := classifyDrop(f, conf)
	if d == nil {
		log.Printf("ammit: keeper: %s blocked twice the same way, and the watchdog would not call %s "+
			"a drop (%s); resumed as before", ticket, shortRun(f.run), why)
		return false
	}
	// No code fix brings a network back: resumed, as before, until access answers.
	if networkRefused(d) {
		return false
	}
	wdSetMeta("escalated:"+f.run, wdNow())
	text := fmt.Sprintf("keeper: %s ended BLOCKED twice in a row the same way (%s, then %s: %q); "+
		"handed to the watchdog as a fix instead of another resume", ticket, shortRun(prev),
		shortRun(f.run), key)
	limit, on := watchdogCap(conf)
	if !on || conf.str("watchdog", "fix_mode", "watchdogfix") == "off" {
		text += " - but the fixer is off (loops.laps_watchdog unset or watchdog.fix_mode off); left for a person"
		judge("run", f.run, ticket, "keeper.escalate", 2, 2, "notify", text)
		store(event{"kind": "keeper", "run": f.run, "text": text})
		log.Printf("ammit: %s", text)
		return true
	}
	d.evidence = append(d.evidence, fmt.Sprintf(
		"the keeper saw the same BLOCKED on consecutive runs %s and %s: %s", prev, f.run, key))
	root, _ := rootOf(f.run)
	recordDrop(d, root) // false when the watchdog recorded it already: the same drop
	judge("run", f.run, ticket, "keeper.escalate", 2, 2, "watchdog", key)
	store(event{"kind": "keeper", "run": f.run, "text": text})
	log.Printf("ammit: %s", text)
	startCycle(d, root, conf, limit)
	return true
}
