package main

import "encoding/json"

// What each limit is called when a person reads it. The key is what the
// config, the events and the queries say - limits.usd_per_run - and that
// stays the key everywhere a machine looks; the title is what the charts
// draw beside the dashed line and what the Limits page prints above the
// field. One map, served to both pages, so the two never drift.
var limitTitles = map[string]string{
	"limits.usd_per_run":          "Cost cap per run",
	"limits.turns_per_run":        "Turn cap per run",
	"limits.turns_per_run_min":    "Turn floor per run",
	"limits.turns_per_session":    "Turn cap per session",
	"limits.turn_tokens":          "Context cap per turn",
	"limits.memory_mb":            "Memory cap per container",
	"limits.net_probe_ms":         "Network probe ceiling",
	"limits.quiet_cpu":            "Quiet CPU threshold",
	"limits.retry_max":            "Revival cap",
	"loops.heal_laps_per_branch":  "Heal lap cap per branch",
	"limits.spin_window":          "Spin window",
	"limits.spin_requests":        "Spin request cap",
	"timeouts.run":                "Run timeout",
	"timeouts.phase":              "Phase timeout",
	"timeouts.session":            "Session timeout",
	"timeouts.turn":               "Turn timeout",
	"timeouts.request":            "Request timeout",
	"timeouts.request_tool":       "Tool request timeout",
	"timeouts.module":             "Module timeout",
	"timeouts.deploy":             "Deploy timeout",
	"timeouts.test":               "Test timeout",
	"timeouts.heartbeat":          "Heartbeat timeout",
	"timeouts.worker_gone":        "Worker-gone timeout",
	"timeouts.turns_per_session":  "Turns-per-session timeout",
}

// limitTitlesJS is the map as a JavaScript literal, for the pages.
func limitTitlesJS() string {
	b, _ := json.Marshal(limitTitles)
	return string(b)
}
