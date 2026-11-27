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
	wdPutMeta("keep_at", wdNow())
	list := strings.Split(tickets, ",")
	if strings.TrimSpace(tickets) == "all" {
		list = recentTickets()
	}
	for _, ticket := range list {
		if ticket = strings.TrimSpace(ticket); ticket != "" {
			keepOne(ticket, conf)
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
	nameRows, err := db.Query(`SELECT DISTINCT name FROM runs WHERE started > ?
	                       AND name ~ '^[A-Z].*-[0-9]' AND name NOT ILIKE 'WD-%'
	                       AND name NOT ILIKE 'ADHOC-%'`, wdNow()-2*86400)
	if err != nil {
		return nil
	}
	defer nameRows.Close()
	var out []string
	for nameRows.Next() {
		var n string
		if nameRows.Scan(&n) == nil {
			out = append(out, n)
		}
	}
	_ = nameRows.Err() // a cut-off read is the shorter list it always was
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
	facts, err := readRunFacts(run)
	if err != nil {
		return
	}
	// A run a person stopped is not lying: it was put down. 2026-10-27: the
	// keeper resumed APF-3301's 13c9a745, stopped byHand hand a minute earlier
	// because a later run had superseded it.
	// A stop through the host orchestrator's own /cancel is a person's too,
	// though no /act judgement says so (stoppedByHand). 2026-11-06: this
	// queued 3f7ba7eb twenty seconds after e4420a99 of APF-1972 was put down
	// that way, and the same hand put the resume down two minutes later.
	if _, byHand := stoppedByHand(facts); byHand {
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
	phase := resumeFrom(facts)
	if handToFixer(ticket, facts, phase, conf) {
		return
	}
	payloadMap := map[string]any{"key": ticket, "rerun": "resume", "source_run": run}
	if facts.mode != "" {
		payloadMap["mode"] = facts.mode
	}
	if phase != "" {
		payloadMap["from_phase"] = phase
	}
	for field, fieldValue := range runParams(run) {
		payloadMap[field] = fieldValue
	}
	payload, _ := json.Marshal(payloadMap)
	// A resume the orchestrator refuses is no resume: three refused in a row
	// for the same ended run, and the keeper says so once, as an ALERT, and
	// stops asking - the refusal is the flow's or the stand's, and a person or
	// a commit must change it. 2026-11-24 18:30 to 2026-11-25 09:29 UTC: the
	// flow lock refused all 1750 resumes of APF-3541 (no
	// loops.laps_contractsfind), each closed in silence, and nobody heard.
	if refusedResumes(ticket, run) >= 3 {
		alertRefusedOnce(ticket, run, conf)
		return
	}
	// Through ticket_actions (actions.go): a ticket a watchdog cycle is
	// fixing, a stop is closing or another resume is queued for is held, and
	// the keeper leaves it to its holder. 2026-11-07: the keeper resumed
	// APF-3313 a minute into cycle 3's fix run WD-9519cad8-3, the resume hit
	// the same red gate, and the cycle was spent before its PR was merged.
	queueID, holder := queueUnderAction(ticket, "keeper", run, string(payload))
	if queueID == 0 {
		sayHeldOnce("keeper", ticket, holder)
		return
	}
	note := fmt.Sprintf("keeper: %s was lying since run %s ended %s; resume queued from %s (queue %d)",
		ticket, shortRun(run), orDash(verdict), orDash(phase), queueID)
	store(event{"kind": "keeper", "run": run, "text": note})
	log.Printf("ammit: %s", note)
}

// What a BLOCKED run is compared on: its stored summary with run ids, numbers
// and spacing taken out, and the phase a resume of it would start from. "gate
// funcreqrules red on 3 branch(es) in 4ef880b4" and the same line a run later
// with 5 branches and another id are one blocker.
var blockedNoise = regexp.MustCompile(`\b[0-9a-f]{8,}\b|[0-9]+(\.[0-9]+)?`)

func blockedKey(summary, phase string) string {
	normalized := blockedNoise.ReplaceAllString(strings.ToLower(summary), "#")
	return strings.Join(strings.Fields(normalized), " ") + " @ " + orDash(phase)
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
	prevFacts, err := readRunFacts(prev)
	if err != nil {
		return false
	}
	blocked := blockedKey(f.summary, phase)
	if blocked != blockedKey(prevFacts.summary, resumeFrom(prevFacts)) {
		return false
	}
	if _, done := wdMeta("escalated:" + f.run); done {
		return true
	}
	// Twice BLOCKED on nothing but unverified data: neither a resume nor a
	// fix puts rows on a stand, so the ticket is a person's (classifyDrop,
	// dataGapsOnly). Said once, as an ALERT, and left lying.
	if dataGapsOnly(f.summary) {
		wdSetMeta("escalated:"+f.run, wdNow())
		note := fmt.Sprintf("keeper: %s ended BLOCKED twice on data gaps only (%s, then %s: %q); "+
			"no resume and no fix changes a stand's data - left for a person", ticket,
			shortRun(prev), shortRun(f.run), f.summary)
		ctx := map[string]string{"run": f.run, "name": ticket, "verdict": "DATA-GAPS",
			"summary": note}
		for field, fieldValue := range conf["context"] {
			if ctx[field] == "" {
				ctx[field] = fieldValue
			}
		}
		judge("run", f.run, ticket, "keeper.escalate", 2, 2, "notify", act("notify", conf, ctx))
		store(event{"kind": "keeper", "run": f.run, "text": note})
		return true
	}
	drop, why := classifyDrop(f, conf)
	if drop == nil {
		log.Printf("ammit: keeper: %s blocked twice the same way, and the watchdog would not call %s "+
			"a drop (%s); resumed as before", ticket, shortRun(f.run), why)
		return false
	}
	// No code fix brings a network back: resumed, as before, until access answers.
	if networkRefused(drop) {
		return false
	}
	wdSetMeta("escalated:"+f.run, wdNow())
	note := fmt.Sprintf("keeper: %s ended BLOCKED twice in a row the same way (%s, then %s: %q); "+
		"handed to the watchdog as a fix instead of another resume", ticket, shortRun(prev),
		shortRun(f.run), blocked)
	limit, capped := watchdogCap(conf)
	if !capped || conf.str("watchdog", "fix_mode", "watchdogfix") == "off" {
		note += " - but the fixer is off (loops.laps_watchdog unset or watchdog.fix_mode off); left for a person"
		judge("run", f.run, ticket, "keeper.escalate", 2, 2, "notify", note)
		store(event{"kind": "keeper", "run": f.run, "text": note})
		log.Printf("ammit: %s", note)
		return true
	}
	drop.evidence = append(drop.evidence, fmt.Sprintf(
		"the keeper saw the same BLOCKED on consecutive runs %s and %s: %s", prev, f.run, blocked))
	root, _ := rootOf(f.run)
	recordDrop(drop, root) // false when the watchdog recorded it already: the same drop
	judge("run", f.run, ticket, "keeper.escalate", 2, 2, "watchdog", blocked)
	store(event{"kind": "keeper", "run": f.run, "text": note})
	log.Printf("ammit: %s", note)
	startCycle(drop, root, conf, limit)
	return true
}

// refusedResumes is how many keeper resumes of run were closed without
// becoming a run since the last one that did.
func refusedResumes(ticket, run string) int {
	mu.Lock()
	defer mu.Unlock()
	var n int
	db.QueryRow(`SELECT count(*) FROM ticket_actions WHERE ticket=? AND run=? AND owner='keeper'
	               AND kind='resume' AND state='failed'
	               AND opened > coalesce((SELECT max(opened) FROM ticket_actions WHERE ticket=?
	                 AND run=? AND owner='keeper' AND kind='resume' AND state='done'), 0)`,
		ticket, run, ticket, run).Scan(&n)
	return n
}

// alertRefusedOnce raises the ALERT for a run whose resumes are all refused,
// once per run, with the last refusal's own words.
func alertRefusedOnce(ticket, run string, conf Config) {
	refusedKey := "refused:" + run
	if _, alerted := wdMeta(refusedKey); alerted {
		return
	}
	wdSetMeta(refusedKey, wdNow())
	var why string
	mu.Lock()
	db.QueryRow(`SELECT coalesce(why,'') FROM ticket_actions WHERE ticket=? AND run=? AND owner='keeper'
	               AND kind='resume' AND state='failed' ORDER BY opened DESC LIMIT 1`, ticket, run).Scan(&why)
	mu.Unlock()
	note := fmt.Sprintf("keeper: %s cannot be resumed - the last three resumes of run %s were refused "+
		"without becoming a run; the keeper stops asking until a run of it starts. Last refusal: %s",
		ticket, shortRun(run), why)
	ctx := map[string]string{"run": run, "name": ticket, "verdict": "RESUME-REFUSED", "summary": note}
	for field, fieldValue := range conf["context"] {
		if ctx[field] == "" {
			ctx[field] = fieldValue
		}
	}
	judge("run", run, ticket, "keeper.refused", 3, 3, "notify", act("notify", conf, ctx))
	store(event{"kind": "keeper", "run": run, "text": note})
	log.Printf("ammit: %s", note)
}
