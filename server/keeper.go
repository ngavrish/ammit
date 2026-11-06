package main

import (
	"encoding/json"
	"fmt"
	"log"
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
			keepOne(t)
		}
	}
}

// recentTickets is every ticket with a run started in the last two days:
// watchdog.keep "all" watches each run without anyone listing its ticket.
// 2026-11-05: keep was empty, so the keeper watched nothing, and APF-1972
// lay stopped while a person asked in a chat why nothing picked it up.
func recentTickets() []string {
	ctx, cancel := readCtx(bg)
	defer cancel()
	rows, err := readDB.QueryContext(ctx, `SELECT DISTINCT name FROM runs WHERE started > ?
	                       AND name GLOB '[A-Z]*-[0-9]*' AND name NOT LIKE 'WD-%'`, wdNow()-2*86400)
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
	if err := rows.Err(); err != nil {
		log.Printf("ammit: keeper: %v", err)
		return nil
	}
	return out
}

func keepOne(ticket string) {
	ctx, cancel := readCtx(bg)
	var busy int
	readDB.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM runs WHERE name=? AND finished IS NULL)
	                  + (SELECT count(*) FROM queue WHERE name=? AND state IN ('waiting','running'))`,
		ticket, ticket).Scan(&busy)
	var run, verdict, summary string
	readDB.QueryRowContext(ctx, `SELECT run, coalesce(verdict,''), coalesce(summary,'') FROM runs
	             WHERE name=? AND finished IS NOT NULL ORDER BY started DESC LIMIT 1`,
		ticket).Scan(&run, &verdict, &summary)
	cancel()
	if busy > 0 || run == "" {
		return
	}
	// Every account spent: wait for the hour the runner named, not restart
	// into the same refusal every quarter of an hour.
	if poolBack(run) > wdNow() {
		return
	}
	switch {
	case verdict == "PASS" || verdict == "FAIL":
		return
	case strings.Contains(summary, "over its ticket budget"):
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
	for _, j := range f.judgements {
		if j.scope == "hand" && stops(j.action) {
			return
		}
		// Over budget is a person's decision, read from the judgement and not
		// from the summary: the runner's run_end lands after ammit's own close
		// and rewrites it. 2026-10-28 01:28: 5a3d7c35 of APF-3122 was closed
		// "over its ticket budget", its run_end six seconds later made the
		// summary "ammit stopped this run before implrulefix", and the keeper
		// queued a resume fourteen seconds after that.
		if j.rule == TicketBudgetRule {
			return
		}
	}
	phase := resumeFrom(f)
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
	db.Exec(`INSERT INTO queue (name, payload, requested) VALUES (?,?,?)`, ticket, string(payload), wdNow())
	text := fmt.Sprintf("keeper: %s was lying since run %s ended %s; resume queued from %s",
		ticket, shortRun(run), orDash(verdict), orDash(phase))
	store(event{"kind": "keeper", "run": run, "text": text})
	log.Printf("ammit: %s", text)
}
