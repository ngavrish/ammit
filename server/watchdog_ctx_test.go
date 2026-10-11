package main

import (
	"strings"
	"testing"
)

// Every watchdog command runs through the runner, so every one of them names
// {worker}, and {worker} only comes from limits.yml's `context`. watchdog_pr
// was filled from it and watchdog_merge was not: on ammit#79 (run fa72bb3b)
// and agentic-v-model#622 (run dc6e5cbd, cycle 2) CI went green and the merge
// was answered "refused: {worker} was never filled - watchdog_merge would have
// been a no-op", the cycle waited out timeouts.watchdog_ci and failed as "still
// not merged". The templates below are the ones agentic-v-model's
// deploy/limits.yml runs.
func TestWatchdogCommandsAreFilledFromContext(t *testing.T) {
	saved := dryRun
	dryRun = true
	defer func() { dryRun = saved }()

	conf := Config{
		"commands": {
			"watchdog_merge":  "docker exec {worker} gh pr merge {pr} --squash --delete-branch",
			"watchdog_deploy": "docker exec {worker} echo deploy {pr}",
		},
		"context": {"worker": "deploy-agent-runner-1"},
	}
	pr := "https://github.com/ngavrish/agentic-v-model/pull/622"
	call := map[string]string{"pr": pr, "run": "dc6e5cbd", "name": "APF-3296"}

	// What the merge was called with before: the refusal the record holds.
	before := act("watchdog_merge", conf, map[string]string{"pr": pr, "run": "dc6e5cbd",
		"name": "APF-3296"})
	if !strings.HasPrefix(before, "refused: {worker} was never filled") {
		t.Fatalf("the call without context should be the refusal run dc6e5cbd got, got %q", before)
	}

	for _, name := range []string{"watchdog_merge", "watchdog_deploy"} {
		ctx := map[string]string{}
		for k, v := range call {
			ctx[k] = v
		}
		got := act(name, conf, wdCtx(conf, ctx))
		if !strings.HasPrefix(got, "[dry run] docker exec deploy-agent-runner-1 ") ||
			!strings.Contains(got, pr) {
			t.Errorf("%s: want the command filled with the worker and the PR, got %q", name, got)
		}
	}

	if _, err := command("watchdog_merge", conf, wdCtx(conf, map[string]string{"pr": pr})); err == nil ||
		!strings.Contains(err.Error(), "docker exec deploy-agent-runner-1 gh pr merge") {
		t.Errorf("command(): want the filled template from the dry run, got %v", err)
	}
}
