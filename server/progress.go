package main

import (
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Progress, on the clock rather than on the run's say-so.
//
// Everything else here speaks when something happens: a phase ends, a limit
// is crossed. A run that is merely slow says nothing at all, and the person
// waiting on it learns how it is going only by asking. On 2026-10-15 the
// watchdog's fix session for d92fd23d sat inside one phase waiting for CI
// checks that the repository does not have; no phase changed, no limit was
// crossed, and nobody heard a word until someone went looking. So: every
// timeouts.progress seconds, one `progress` event per open run, saying where
// it is, for how long, what its last gate said and what each agent is doing
// right now - whether or not anything changed.

const progressDefault = 600

// progressLoop never returns. One round per period; a round reads, writes one
// event per run, and sleeps.
func progressLoop(confPath string) {
	for {
		every := float64(progressDefault)
		if conf := loadConfig(confPath); len(conf) > 0 {
			if n, ok := conf.num("timeouts", "progress"); ok && n > 0 {
				every = n
			}
		}
		time.Sleep(time.Duration(every) * time.Second)
		for _, r := range openRuns() {
			store(event{"kind": "progress", "run": r.run, "text": progressText(r)})
		}
	}
}

// progressText is one run's state as a few lines of prose.
func progressText(r openRun) string {
	now := nowWall()
	lines := []string{fmt.Sprintf("%s %s: %s in, $%.2f, %d turns",
		orDash(r.name), run8(r.run), minutes(now-r.started), r.usd, r.turns)}
	phases := currentPhases(r.run)
	if len(phases) == 0 {
		lines = append(lines, "no phase open")
	}
	for _, p := range phases {
		lines = append(lines, fmt.Sprintf("in %s for %s (lap %d)", p.key, minutes(now-p.at), p.lap))
	}
	if phase, text, at := lastRed(r.run); phase != "" {
		lines = append(lines, fmt.Sprintf("last red: %s, %s ago: %s", phase, minutes(now-at), clip(text, 200)))
	}
	for _, c := range inFlight(r.run) {
		lines = append(lines, fmt.Sprintf("%s waiting %s on: %s", c.agent, minutes(now-c.at), clip(c.doing, 160)))
	}
	if pulse := heartbeatAge(r.run); pulse >= 0 {
		lines = append(lines, fmt.Sprintf("heartbeat %s ago", minutes(pulse)))
	}
	return strings.Join(lines, "\n")
}

type livePhase struct {
	key string
	at  float64
	lap int
}

// currentPhases is every phase@branch whose LAST start has no end after it.
// openPhases answers a different question - a phase with any end at all is
// closed there - and a loop's second lap (reportrules, foldgate) is exactly
// the phase this report exists to show.
func currentPhases(run string) []livePhase {
	mu.Lock()
	defer mu.Unlock()
	rows, err := db.Query(`
		SELECT coalesce(s.phase,''), coalesce(s.branch,''), max(s.at), count(*) FROM events s
		WHERE s.run=? AND s.kind='phase_start' AND ifnull(s.phase,'') <> ''
		GROUP BY 1, 2
		HAVING max(s.id) > coalesce((SELECT max(e.id) FROM events e
		        WHERE e.run=s.run AND e.kind='phase_end'
		          AND coalesce(e.phase,'')=coalesce(s.phase,'')
		          AND coalesce(e.branch,'')=coalesce(s.branch,'')), 0)
		ORDER BY 3`, run)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []livePhase
	for rows.Next() {
		var phase, branch string
		var p livePhase
		if rows.Scan(&phase, &branch, &p.at, &p.lap) == nil {
			p.key = phaseKey(phase, branch)
			out = append(out, p)
		}
	}
	return out
}

// lastRed is the newest phase end that said it failed or was red.
func lastRed(run string) (string, string, float64) {
	mu.Lock()
	defer mu.Unlock()
	var phase, text string
	var at float64
	err := db.QueryRow(`SELECT coalesce(phase,''), coalesce(json_extract(payload,'$.text'),
	                           json_extract(payload,'$.error'),''), at
	                    FROM events WHERE run=? AND kind='phase_end'
	                      AND (json_extract(payload,'$.ok') IN (0, 'false')
	                           OR json_extract(payload,'$.failed') IN (1, 'true')
	                           OR json_extract(payload,'$.text') LIKE '%RED%')
	                    ORDER BY id DESC LIMIT 1`, run).Scan(&phase, &text, &at)
	if err != nil {
		return "", "", 0
	}
	return phase, text, at
}

type liveCall struct {
	agent string
	at    float64
	doing string
}

// inFlight is each open wait of the run with what its agent last asked for.
func inFlight(run string) []liveCall {
	spans := openSpans(run, "request_start", "request_end", "session")
	out := make([]liveCall, 0, len(spans))
	for request := range spans {
		if spanIsOrphaned(run, request) {
			continue
		}
		agent, branch, at := spanFacts(run, request)
		if branch != "" {
			agent += "@" + branch
		}
		doing := lastAsked(run, request)
		if doing == "" {
			doing = "the model"
		}
		out = append(out, liveCall{agent, at, doing})
	}
	return out
}

// lastAsked is the last tool line the request's agent logged before the wait
// opened: "Bash {...}" for a shell call. Empty when it logged none.
func lastAsked(run, request string) string {
	mu.Lock()
	defer mu.Unlock()
	var started float64
	var agent string
	if db.QueryRow(`SELECT at, coalesce(agent,'') FROM events
	                WHERE run=? AND kind='request_start' AND session=?
	                ORDER BY id DESC LIMIT 1`, run, request).Scan(&started, &agent) != nil {
		return ""
	}
	var text sql.NullString
	db.QueryRow(`SELECT json_extract(payload,'$.text') FROM events
	             WHERE run=? AND kind='log' AND agent=? AND at<=?
	               AND json_extract(payload,'$.level')='tool'
	             ORDER BY id DESC LIMIT 1`, run, agent, started).Scan(&text)
	return text.String
}

// A wait dressed as work. The hour a tool call is allowed is for a browser
// suite; an agent that spends it in `sleep 90`, `gh run watch` or a polling
// loop is waiting on something outside the run, and the turn limit is the one
// that fits - a session silent that long is a session to look at. The fix
// session for d92fd23d waited on CI checks the repository does not have, in
// one Bash call after another, under the hour ceiling.
var (
	waitWords = regexp.MustCompile(`(?i)\bgh\s+(run|pr)\s+(watch|checks\b.{0,200}?--watch)|\binotifywait\b|\btail\s+-f\b|\b(until|while|for)\b.{0,400}?\bdo\b.{0,400}?\bsleep\b`)
	longSleep = regexp.MustCompile(`(?i)\bsleep\s+(\d+)`)
)

// waitingCall says whether the request's open tool call is a wait, and what it is.
func waitingCall(run, request string) (bool, string) {
	doing := lastAsked(run, request)
	if !strings.HasPrefix(doing, "Bash") {
		return false, ""
	}
	if waitWords.MatchString(doing) {
		return true, doing
	}
	for _, m := range longSleep.FindAllStringSubmatch(doing, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n >= 60 {
			return true, doing
		}
	}
	return false, ""
}

func run8(run string) string {
	if len(run) > 8 {
		return run[:8]
	}
	return run
}

func minutes(seconds float64) string {
	if seconds < 60 {
		return fmt.Sprintf("%.0fs", seconds)
	}
	return fmt.Sprintf("%.0fm", seconds/60)
}

func clip(text string, max int) string {
	text = firstLine(strings.TrimSpace(text))
	if len(text) > max {
		text = text[:max]
	}
	return text
}
