package main

import (
	"database/sql"
	"fmt"
	"io"
	"strings"
	"time"
)

// `ammit watchdog-history <db> <days>`: the watchdog's drop detector, run
// read-only over a database's past runs. Prints, for every run started in
// the window, whether the watchdog would have called it a drop, why, and the
// phase a resume would have started from. Nothing is written: the database is
// opened read-only and no cycle is started.
//
// It exists because a detector is proved on the runs that actually dropped,
// not on runs a test made up.
func watchdogHistory(out io.Writer, dbPath string, days float64, conf Config) error {
	var err error
	db, err = sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(30000)")
	if err != nil {
		return err
	}
	since := float64(time.Now().Add(-time.Duration(days*24) * time.Hour).Unix())
	rows, err := db.Query(`SELECT run FROM runs WHERE started > ? ORDER BY started`, since)
	if err != nil {
		return err
	}
	var runs []string
	for rows.Next() {
		var r string
		if rows.Scan(&r) == nil {
			runs = append(runs, r)
		}
	}
	rows.Close()
	drops, waits := 0, 0
	for _, run := range runs {
		f, err := readRunFacts(run)
		if err != nil {
			continue
		}
		// Read as the live tick would have read it: within two ticks
		// of the run's end, before anybody started the ticket again.
		f.seenAt = f.finished + 40
		when := time.Unix(int64(f.started), 0).UTC().Format("2006-01-02 15:04")
		d, why := classifyDrop(f, conf)
		// The waiting half, replayed: the longest stretch this run went with
		// no progress event, against the limit live ticks would have held it to.
		if gap, phase, ok := longestStall(run, endOf(f)); ok {
			limit, has := conf.num("limits", "wait")
			if own, hasOwn := conf.num("limits", "wait_"+phase); hasOwn {
				limit, has = own, true
			}
			if has && gap > limit {
				waits++
				_, _ = fmt.Fprintf(out, "%s  %s  %-9s %-8s  WAIT   no progress for %.0fs in %s (limit %.0fs)\n",
					when, shortRun(run), f.name, orDash(f.verdict), gap, orDash(phase), limit)
			}
		}
		if d == nil {
			_, _ = fmt.Fprintf(out, "%s  %s  %-9s %-8s  skip   %s\n", when, shortRun(run), f.name,
				orDash(f.verdict), why)
			continue
		}
		drops++
		_, _ = fmt.Fprintf(out, "%s  %s  %-9s %-8s  DROP   cause=%s  resume_from=%s  mode=%s\n",
			when, shortRun(run), f.name, orDash(f.verdict), d.cause, orDash(d.phase), orDash(d.mode))
		for _, line := range d.evidence[1:] {
			if len(line) > 160 {
				line = line[:160]
			}
			_, _ = fmt.Fprintf(out, "      %s\n", strings.ReplaceAll(line, "\n", " "))
		}
	}
	_, _ = fmt.Fprintf(out, "%d run(s), %d drop(s), %d wait(s)\n", len(runs), drops, waits)
	return nil
}

// longestStall is the longest gap between two progress events of a run, or
// between its start and its first one, and the phase it was in at the time.
func longestStall(run string, until float64) (float64, string, bool) {
	var gap float64
	var phase sql.NullString
	err := db.QueryRow(`SELECT gap, phase FROM (
	        SELECT at - lag(at) OVER (ORDER BY at) AS gap,
	               lag(phase) OVER (ORDER BY at) AS phase
	        FROM events WHERE run=? AND at <= ? AND kind IN ('`+strings.Join(wdProgress, "','")+`'))
	      WHERE gap IS NOT NULL ORDER BY gap DESC LIMIT 1`, run, until).Scan(&gap, &phase)
	if err != nil {
		return 0, "", false
	}
	return gap, phase.String, true
}

// endOf is when a run stopped being able to make progress: its end, or now.
func endOf(f *runFacts) float64 {
	if f.open {
		return wdNow()
	}
	return f.finished
}
