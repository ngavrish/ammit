package main

// The reporter is the watchdog that only speaks. The keeper resumes a ticket,
// the fixer repairs the flow, and a person still learned where a ticket stood
// by asking: on 2026-11-17 APF-3499 sat BLOCKED from 01:17 to 07:51 UTC with
// nobody told. Every reporter.every_s (600 by default) it posts one message
// with every active ticket - its run, its phase or the reason it stopped, its
// watchdog cycle and what waits in the queue - through commands.report_status.
// Nothing active, nothing sent. It is a report, like warn: actions.enforce
// does not silence it.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

const (
	reporterEvery  = 600.0
	reporterRecent = 12 * 3600.0
	reporterMax    = 3900 // under Telegram's 4096 per message
)

func reporterLoop(confPath string) {
	for {
		conf := loadConfig(confPath)
		every := reporterEvery
		if n, ok := conf.num("reporter", "every_s"); ok && n > 0 {
			every = n
		}
		time.Sleep(time.Duration(every) * time.Second)
		conf = loadConfig(confPath)
		if conf.str("reporter", "enabled", "true") == "false" {
			continue
		}
		reportOnce(conf)
	}
}

// reportOnce builds and sends one status message; how many tickets it named.
func reportOnce(conf Config) int {
	recent := reporterRecent
	if n, ok := conf.num("reporter", "recent_s"); ok && n > 0 {
		recent = n
	}
	tickets := activeTickets(nowWall() - recent)
	if len(tickets) == 0 {
		return 0
	}
	var blocks []string
	for _, t := range tickets {
		if b := ticketStatus(t, nowWall()-recent); b != "" {
			blocks = append(blocks, b)
		}
	}
	if len(blocks) == 0 {
		return 0
	}
	text := "Статус тикетов, " + time.Now().UTC().Format("15:04") + " UTC\n\n" + strings.Join(blocks, "\n\n")
	// A {word} in a summary would read as an unfilled hole to the command check.
	text = strings.NewReplacer("{", "(", "}", ")").Replace(text)
	if len(text) > reporterMax {
		text = text[:reporterMax] + "…"
	}
	out, err := command("report_status", conf, map[string]string{"text": text})
	if err != nil {
		log.Printf("ammit: reporter could not send: %v %s", err, strings.TrimSpace(out))
		return 0
	}
	log.Printf("ammit: reporter sent %d ticket(s)", len(blocks))
	return len(blocks)
}

// activeTickets are the tickets (never a WD-* fix run) with an open run, a
// last run that stopped BLOCKED or UNKNOWN since `since`, a moving watchdog
// cycle, or a last run that finished since `since` and was not yet reported
// done.
func activeTickets(since float64) []string {
	mu.Lock()
	defer mu.Unlock()
	rows, err := db.Query(`
		SELECT name FROM (
		  SELECT DISTINCT ON (name) name, finished, coalesce(verdict,'') AS verdict
		  FROM runs WHERE name NOT LIKE 'WD-%' AND name <> run
		  ORDER BY name, started DESC) last
		WHERE finished IS NULL OR finished > ?
		UNION
		SELECT r.name FROM watchdog_cycles c JOIN runs r ON r.run = c.run
		WHERE c.state IN ('fixing','ci','merging','deploying','resumed')
		  AND r.name NOT LIKE 'WD-%'
		ORDER BY 1`, since)
	if err != nil {
		log.Printf("ammit: reporter: %v", err)
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil && n != "" {
			out = append(out, n)
		}
	}
	_ = rows.Err() // a cut-off read names fewer tickets, never a wrong one
	return out
}

type lastRun struct {
	run, verdict, summary string
	started, usd          float64
	finished              *float64
}

// ticketStatus is one ticket's block, or "" when there is nothing to say
// (a finished run already reported done).
func ticketStatus(ticket string, since float64) string {
	r, ok := latestRun(ticket)
	if !ok {
		return ""
	}
	var lines []string
	head := ticket + " " + shortRun(r.run) + ": "
	switch {
	case r.finished == nil:
		phase, at := lastPhase(r.run)
		lines = append(lines, fmt.Sprintf("%sидёт: фаза %s с %s UTC, %d мин, $%.0f", head, orDash(phase),
			clock(at), int((nowWall()-r.started)/60), r.usd))
	case r.verdict == "BLOCKED" || r.verdict == "UNKNOWN" || r.verdict == "":
		lines = append(lines, fmt.Sprintf("%sвстал %s в %s: %s", head, orDash(r.verdict), clock(*r.finished),
			headLine(r.summary, 200)))
	default:
		key := "reporter_done:" + r.run
		if _, said := wdMeta(key); said || *r.finished < since {
			return ""
		}
		wdSetMeta(key, nowWall())
		line := fmt.Sprintf("%sготово: %s в %s", head, r.verdict, clock(*r.finished))
		if pr := publishedPR(r.run); pr != "" {
			line += ", PR " + pr
		}
		lines = append(lines, line)
	}
	lines = append(lines, watchdogLines(ticket)...)
	lines = append(lines, queuedLines(ticket)...)
	return strings.Join(lines, "\n")
}

func latestRun(ticket string) (lastRun, bool) {
	mu.Lock()
	defer mu.Unlock()
	var r lastRun
	var fin sql.NullFloat64
	err := db.QueryRow(`SELECT run, coalesce(verdict,''), coalesce(summary,''), started, coalesce(usd,0), finished
	                    FROM runs WHERE name=? ORDER BY started DESC LIMIT 1`, ticket).
		Scan(&r.run, &r.verdict, &r.summary, &r.started, &r.usd, &fin)
	if fin.Valid {
		v := fin.Float64
		r.finished = &v
	}
	return r, err == nil
}

func lastPhase(run string) (string, float64) {
	mu.Lock()
	defer mu.Unlock()
	var phase string
	var at float64
	if db.QueryRow(`SELECT coalesce(phase,''), at FROM events WHERE run=? AND kind='phase_start'
	                ORDER BY at DESC LIMIT 1`, run).Scan(&phase, &at) != nil {
		return "", 0
	}
	return phase, at
}

// publishedPR is the pull request link the publish phase printed, if any.
func publishedPR(run string) string {
	mu.Lock()
	defer mu.Unlock()
	var text string
	if db.QueryRow(`SELECT coalesce(payload->>'text','') FROM events WHERE run=? AND kind='phase_end'
	                AND phase='publish' ORDER BY at DESC LIMIT 1`, run).Scan(&text) != nil {
		return ""
	}
	for _, f := range strings.Fields(text) {
		if strings.HasPrefix(f, "http") && strings.Contains(f, "/pull") {
			return strings.Trim(f, "\"'(),.;")
		}
	}
	return ""
}

func watchdogLines(ticket string) []string {
	mu.Lock()
	defer mu.Unlock()
	rows, err := db.Query(`SELECT c.cycle, c.state, coalesce(c.prs,'') FROM watchdog_cycles c
	                       JOIN runs r ON r.run = c.run
	                       WHERE r.name=? AND c.state IN ('fixing','ci','merging','deploying','resumed')
	                       ORDER BY c.id`, ticket)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var cycle int
		var state, prs string
		if rows.Scan(&cycle, &state, &prs) != nil {
			continue
		}
		line := fmt.Sprintf("  вотчдог: цикл %d %s", cycle, state)
		var urls []string
		if json.Unmarshal([]byte(prs), &urls) == nil && len(urls) > 0 {
			line += " " + strings.Join(urls, " ")
		}
		out = append(out, line)
	}
	_ = rows.Err() // a cut-off read is fewer lines
	return out
}

func queuedLines(ticket string) []string {
	mu.Lock()
	defer mu.Unlock()
	rows, err := db.Query(`SELECT coalesce(payload,'') FROM queue WHERE name=? AND finished IS NULL
	                       AND coalesce(run,'') = '' ORDER BY id`, ticket)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var payload string
		if rows.Scan(&payload) != nil {
			continue
		}
		var p map[string]any
		_ = json.Unmarshal([]byte(payload), &p)
		from, _ := p["from_phase"].(string)
		out = append(out, "  в очереди: "+orDash(from))
	}
	_ = rows.Err() // a cut-off read is fewer lines
	return out
}

func clock(at float64) string {
	if at <= 0 {
		return "-"
	}
	return time.Unix(int64(at), 0).UTC().Format("15:04")
}

func headLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "…"
	}
	return s
}
