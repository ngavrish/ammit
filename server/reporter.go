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
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
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
		if configured, ok := conf.num("reporter", "every_s"); ok && configured > 0 {
			every = configured
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
	if recentSecs, ok := conf.num("reporter", "recent_s"); ok && recentSecs > 0 {
		recent = recentSecs
	}
	tickets := activeTickets(nowWall() - recent)
	if len(tickets) == 0 {
		return 0
	}
	var blocks []string
	ended := false
	for _, ticket := range tickets {
		if block, end := ticketStatus(ticket, nowWall()-recent); block != "" {
			blocks = append(blocks, block)
			ended = ended || end
		}
	}
	if len(blocks) == 0 {
		return 0
	}
	report := "Ticket status, " + time.Now().UTC().Format("15:04") + " UTC\n\n" + strings.Join(blocks, "\n\n")
	// A {word} in a summary would read as an unfilled hole to the command check.
	report = strings.NewReplacer("{", "(", "}", ")").Replace(report)
	if len(report) > reporterMax {
		report = report[:reporterMax] + "…"
	}
	report, entities := mentionOwner(conf, report, ended)
	sent, err := command("report_status", conf, map[string]string{"text": report, "entities": entities})
	if err != nil {
		log.Printf("ammit: reporter could not send: %v %s", err, strings.TrimSpace(sent))
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
	ticketRows, err := db.Query(`
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
	defer ticketRows.Close()
	var out []string
	for ticketRows.Next() {
		var n string
		if ticketRows.Scan(&n) == nil && n != "" {
			out = append(out, n)
		}
	}
	_ = ticketRows.Err() // a cut-off read names fewer tickets, never a wrong one
	return out
}

type lastRun struct {
	run, verdict, summary string
	started, usd          float64
	finished              *float64
}

// mentionOwner puts the owner's name in front of a message that carries a run
// ending (done or stopped), as a Telegram text_mention, so the phone rings: a
// status line in a group chat is silent, and on 2026-11-18 the owner asked to
// be pinged when a ticket reaches an end. TELEGRAM_OWNER_ID (beside the bot's
// token, never in limits.yml, which anyone can GET) names whom, and
// reporter.mention_name what to call them; without an id, or for a message
// that ends nothing, the text goes as it was and entities is "[]".
func mentionOwner(conf Config, text string, ended bool) (string, string) {
	id := strings.TrimSpace(os.Getenv("TELEGRAM_OWNER_ID"))
	if !ended || id == "" {
		return text, "[]"
	}
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		log.Printf("ammit: reporter: TELEGRAM_OWNER_ID is not a number")
		return text, "[]"
	}
	mention := conf.str("reporter", "mention_name", "owner")
	// Telegram counts offsets and lengths in UTF-16 code units.
	mentionLen := len(utf16.Encode([]rune(mention)))
	return mention + ", " + text, fmt.Sprintf(`[{"type":"text_mention","offset":0,"length":%d,"user":{"id":%s}}]`, mentionLen, id)
}

// ticketStatus is one ticket's block, or "" when there is nothing to say
// (a finished run already reported done), and whether the block is the first
// to tell of its run's end.
func ticketStatus(ticket string, since float64) (string, bool) {
	latest, ok := latestRun(ticket)
	if !ok {
		return "", false
	}
	ended := false
	if latest.finished != nil {
		endedKey := "reporter_ended:" + latest.run
		if _, reported := wdMeta(endedKey); !reported {
			wdSetMeta(endedKey, nowWall())
			ended = true
		}
	}
	var lines []string
	prefix := ticket + " " + shortRun(latest.run) + ": "
	switch {
	case latest.finished == nil:
		phase, since := lastPhase(latest.run)
		lines = append(lines, fmt.Sprintf("%srunning: phase %s since %s UTC, %d min, $%.0f", prefix, orDash(phase),
			clock(since), int((nowWall()-latest.started)/60), latest.usd))
	case latest.verdict == "BLOCKED" || latest.verdict == "UNKNOWN" || latest.verdict == "":
		lines = append(lines, fmt.Sprintf("%sstopped %s at %s: %s", prefix, orDash(latest.verdict), clock(*latest.finished),
			headLine(latest.summary, 200)))
	default:
		doneKey := "reporter_done:" + latest.run
		if _, reported := wdMeta(doneKey); reported || *latest.finished < since {
			return "", false
		}
		wdSetMeta(doneKey, nowWall())
		doneLine := fmt.Sprintf("%sdone: %s at %s", prefix, latest.verdict, clock(*latest.finished))
		if prURL := publishedPR(latest.run); prURL != "" {
			doneLine += ", PR " + prURL
		}
		lines = append(lines, doneLine)
	}
	lines = append(lines, watchdogLines(ticket)...)
	lines = append(lines, queuedLines(ticket)...)
	return strings.Join(lines, "\n"), ended
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
		finishedAt := fin.Float64
		r.finished = &finishedAt
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
	for _, word := range strings.Fields(text) {
		if strings.HasPrefix(word, "http") && strings.Contains(word, "/pull") {
			return strings.Trim(word, "\"'(),.;")
		}
	}
	return ""
}

func watchdogLines(ticket string) []string {
	mu.Lock()
	defer mu.Unlock()
	cycleRows, err := db.Query(`SELECT c.cycle, c.state, coalesce(c.prs,'') FROM watchdog_cycles c
	                       JOIN runs r ON r.run = c.run
	                       WHERE r.name=? AND c.state IN ('fixing','ci','merging','deploying','resumed')
	                       ORDER BY c.id`, ticket)
	if err != nil {
		return nil
	}
	defer cycleRows.Close()
	var out []string
	for cycleRows.Next() {
		var cycle int
		var state, prs string
		if cycleRows.Scan(&cycle, &state, &prs) != nil {
			continue
		}
		cycleLine := fmt.Sprintf("  watchdog: cycle %d %s", cycle, state)
		var urls []string
		if json.Unmarshal([]byte(prs), &urls) == nil && len(urls) > 0 {
			cycleLine += " " + strings.Join(urls, " ")
		}
		out = append(out, cycleLine)
	}
	_ = cycleRows.Err() // a cut-off read is fewer lines
	return out
}

func queuedLines(ticket string) []string {
	mu.Lock()
	defer mu.Unlock()
	queueRows, err := db.Query(`SELECT coalesce(payload,'') FROM queue WHERE name=? AND finished IS NULL
	                       AND coalesce(run,'') = '' ORDER BY id`, ticket)
	if err != nil {
		return nil
	}
	defer queueRows.Close()
	var out []string
	for queueRows.Next() {
		var payload string
		if queueRows.Scan(&payload) != nil {
			continue
		}
		var p map[string]any
		_ = json.Unmarshal([]byte(payload), &p)
		from, _ := p["from_phase"].(string)
		out = append(out, "  queued: "+orDash(from))
	}
	_ = queueRows.Err() // a cut-off read is fewer lines
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
	if newline := strings.IndexByte(s, '\n'); newline >= 0 {
		s = s[:newline]
	}
	if runes := []rune(s); len(runes) > max {
		s = string(runes[:max]) + "…"
	}
	return s
}
