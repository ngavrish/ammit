package main

import (
	"strings"
	"testing"
)

// The deploy after agentic-v-model#775's merge left that checkout behind with
// uncommitted changes. Once the deploy says so as a failure, the cycle that
// times out waiting for a success fails with those words, not only "no deploy
// reported success" (chain f47e4c53, cycle 1; deploy run 39861194426).
func TestDeploysSaidCarriesTheDeploysWords(t *testing.T) {
	if err := openDB(t.TempDir() + "/ammit.db"); err != nil {
		t.Fatal(err)
	}
	const merged = 1792705885.0
	rows := []struct {
		at             float64
		run, st, about string
	}{
		{merged - 60, "39860284390", "failure", "before the merge, not counted"},
		{merged + 60, "39861194426", "running", ""},
		{merged + 120, "39861194426", "failure",
			"not deployed: agentic-v-model (main) is 1 commit(s) behind origin/main with uncommitted changes, left alone"},
		{merged + 600, "39863249675", "yielded", ""},
		{merged + 900, "39864836809", "failure", ""},
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO deploys (at, run, status, detail, shas) VALUES (?,?,?,?,?)`,
			r.at, r.run, r.st, r.about, ""); err != nil {
			t.Fatal(err)
		}
	}
	got := deploysSaid(merged)
	for _, want := range []string{
		"2 failure, 1 yielded",
		"(failure, run 39861194426): not deployed: agentic-v-model (main) is 1 commit(s) behind",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("deploysSaid = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "before the merge") {
		t.Errorf("deploysSaid = %q counts a deploy from before the merge", got)
	}
	if got := deploysSaid(merged + 1000); got != "" {
		t.Errorf("deploysSaid with nothing after the merge = %q, want \"\"", got)
	}
}
