package main

import (
	"strings"
	"testing"
)

// The watchdog's merge, as deploy/limits.yml in agentic-v-model writes it and as
// watchdog.go calls it: {pr, run, name} from the cycle, {worker} from context.
// On run 0dd68e70 (APF-3296) this came back "refused: {worker} was never filled
// — watchdog_merge would have been a no-op" and green PR #630 was never merged.
func TestActFillsHolesFromContext(t *testing.T) {
	was := dryRun
	dryRun = true
	defer func() { dryRun = was }()
	conf := Config{
		"commands": {
			"watchdog_merge":  "docker exec {worker} gh pr merge {pr} --squash --delete-branch",
			"watchdog_deploy": "curl -X POST {envd}/deploy -d {pr}",
		},
		"context": {"worker": "agentic-v-model-agent-runner-1", "envd": "http://envd:9100"},
	}
	pr := "https://github.com/ngavrish/agentic-v-model/pull/630"
	ctx := map[string]string{"pr": pr, "run": "0dd68e70", "name": "APF-3296"}

	out := act("watchdog_merge", conf, ctx)
	want := "[dry run] docker exec agentic-v-model-agent-runner-1 gh pr merge " + pr +
		" --squash --delete-branch"
	if out != want {
		t.Fatalf("watchdog_merge:\n got %q\nwant %q", out, want)
	}
	if out := act("watchdog_deploy", conf, ctx); strings.Contains(out, "refused") ||
		!strings.Contains(out, "http://envd:9100/deploy") {
		t.Fatalf("watchdog_deploy: %q", out)
	}
	// command() is the other way a configured command runs.
	if _, err := command("watchdog_merge", conf, ctx); err == nil ||
		!strings.Contains(err.Error(), "agentic-v-model-agent-runner-1") {
		t.Fatalf("command watchdog_merge: %v", err)
	}
	// The caller's map is not changed, and what the caller set wins.
	if _, ok := ctx["worker"]; ok {
		t.Fatal("act wrote context into the caller's map")
	}
	own := map[string]string{"pr": pr, "worker": "other-worker"}
	if out := act("watchdog_merge", conf, own); !strings.Contains(out, "exec other-worker ") {
		t.Fatalf("caller's worker lost: %q", out)
	}
}

// The guard against a hole nobody filled still stands: a key neither the caller
// nor context sets is refused, as before.
func TestActStillRefusesAnUnfilledHole(t *testing.T) {
	was := dryRun
	dryRun = true
	defer func() { dryRun = was }()
	conf := Config{
		"commands": {"stop_run": "docker exec {worker} sh -c 'touch /runs/{name}/.cancel'"},
		"context":  {"worker": "w"},
	}
	out := act("stop_run", conf, map[string]string{"run": "r"})
	if !strings.HasPrefix(out, "refused: {name} was never filled") {
		t.Fatalf("got %q", out)
	}
}
