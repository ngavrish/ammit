package main

import (
	"log"
	"strconv"
	"time"

	_ "modernc.org/sqlite"
)

// The limits, as a record: every number every chart is drawn against, and when it changed.

// limitsSeen is the last value written for each limit, so the table grows when
// something changes rather than on every tick.
var limitsSeen = map[string]struct{ value, at float64 }{}

// recordLimits writes the limits down as a series, so a chart can draw the line
// a run was measured against instead of a number somebody typed into a panel
// months ago. A limit edited mid-run bends its own line at the minute it was
// edited, and the run underneath it is right there to compare.
//
// Every numeric setting is recorded, whatever it is called: this service does
// not get to decide which of somebody's limits are the interesting ones.
func recordLimits(conf Config) {
	now := float64(time.Now().UnixNano()) / 1e9
	for section, kv := range conf {
		switch section {
		case "commands", "actions", "context":
			continue // names of things to run, not numbers to cross
		}
		for key, raw := range kv {
			value, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				continue
			}
			name := section + "." + key
			// A heartbeat every minute as well as on change: a line needs two
			// points, and a chart of the last hour should not be empty because
			// nobody touched the config today.
			if last, ok := limitsSeen[name]; ok && last.value == value && now-last.at < 60 {
				continue
			}
			mu.Lock()
			_, err = db.Exec("INSERT INTO limits (at, name, value) VALUES (?,?,?)",
				now, name, value)
			mu.Unlock()
			if err != nil {
				log.Printf("ammit: could not record limit %s: %v", name, err)
				continue
			}
			limitsSeen[name] = struct{ value, at float64 }{value, now}
		}
	}
}
