package main

import (
	"strings"
	"testing"
)

// The watchdog's merge of PR #631 (chain 0effa7ec, cycle 2) was refused as
// "{worker} was never filled — watchdog_merge would have been a no-op": the
// call passed {pr}, {run} and {name}, and {worker} lives in limits.yml's
// context block. The templates below are the ones agentic-v-model's
// deploy/limits.yml configures.
func TestCommandsAreFilledFromTheContextBlock(t *testing.T) {
	was := dryRun
	dryRun = true
	t.Cleanup(func() { dryRun = was })
	conf := Config{
		"actions": {"enforce": "on"},
		"commands": {
			"watchdog_merge":    "docker exec {worker} gh pr merge {pr} --squash --delete-branch",
			"watchdog_deploy":   "docker exec {worker} deploy {pr}",
			"watchdog_deployed": "docker exec {worker} check {pr}",
			"needs_unknown":     "docker exec {worker} do {nobody}",
		},
		"context": {"worker": "agentic-v-model-agent-runner-1"},
	}
	pr := "https://github.com/ngavrish/agentic-v-model/pull/631"
	ctx := map[string]string{"pr": pr, "run": "0effa7ec", "name": "APF-3296"}

	want := "[dry run] docker exec agentic-v-model-agent-runner-1 gh pr merge " + pr +
		" --squash --delete-branch"
	if got := act("watchdog_merge", conf, ctx); got != want {
		t.Fatalf("watchdog_merge: got %q, want %q", got, want)
	}
	if got := act("watchdog_deploy", conf, ctx); strings.Contains(got, "never filled") ||
		!strings.Contains(got, "agentic-v-model-agent-runner-1") {
		t.Fatalf("watchdog_deploy was not filled from context: %q", got)
	}
	if _, err := command("watchdog_deployed", conf, ctx); err == nil ||
		!strings.Contains(err.Error(), "docker exec agentic-v-model-agent-runner-1 check") {
		t.Fatalf("watchdog_deployed was not filled from context: %v", err)
	}

	// A value the caller names wins over the context block.
	if got := act("watchdog_merge", conf, map[string]string{"pr": pr, "worker": "w2"}); !strings.Contains(got, "exec w2 ") {
		t.Fatalf("caller's worker did not win: %q", got)
	}
	// The caller's map is not written to.
	if _, ok := ctx["worker"]; ok {
		t.Fatal("act() wrote the context into the caller's map")
	}

	// A hole neither the caller nor the context fills is still refused.
	if got := act("needs_unknown", conf, ctx); !strings.HasPrefix(got, "refused: {nobody} was never filled") {
		t.Fatalf("an unfilled hole was not refused: %q", got)
	}
	if _, err := command("needs_unknown", conf, ctx); err == nil ||
		!strings.Contains(err.Error(), "{nobody} was never filled") {
		t.Fatalf("command(): an unfilled hole was not refused: %v", err)
	}
	// And with no context block at all, the original refusal stands.
	bare := Config{"actions": {"enforce": "on"}, "commands": conf["commands"]}
	if got := act("watchdog_merge", bare, ctx); !strings.HasPrefix(got, "refused: {worker} was never filled") {
		t.Fatalf("no context block, yet no refusal: %q", got)
	}
}
