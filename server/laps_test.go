package main

import "testing"

// A repair cap counts the laps of one attempt. On APF-3296 (run fa72bb3b)
// claim-8 ran implementing, two implrulefix laps, gave up, got its one fresh
// attempt (implementing again) and entered its first repair of that attempt
// at 21:25:20. Counted over the run that was a third implrulefix against a cap
// of two, and the branch was stopped 18 s into the lap that was to reproduce
// its fix live; the run then dropped as limit:loops.laps_implrulefix. The
// timestamps below are claim-8's phase_start events from that run's record.
func TestRepairLapsAreCountedPerAttempt(t *testing.T) {
	if err := openDB(t.TempDir() + "/ammit.db"); err != nil {
		t.Fatal(err)
	}
	const run = "fa72bb3b"
	starts := []struct {
		branch, phase string
		at            float64
	}{
		// claim-8: two laps in its first attempt, one in its fresh attempt.
		{"claim-8", "implementing", 1791655333.08},
		{"claim-8", "implrulefix", 1791663041.67},
		{"claim-8", "implrulefix", 1791666051.85},
		{"claim-8", "implementing", 1791666377.20},
		{"claim-8", "implrulefix", 1791667520.61},
		// A branch past its cap inside one attempt is still stopped.
		{"claim-9", "implementing", 1791655333.08},
		{"claim-9", "implrulefix", 1791663041.67},
		{"claim-9", "implrulefix", 1791666051.85},
		{"claim-9", "implrulefix", 1791667520.61},
		// A branch with no attempt phase is counted over the whole run.
		{"claim-10", "implrulefix", 1791663041.67},
		{"claim-10", "implrulefix", 1791666051.85},
		{"claim-10", "implrulefix", 1791667520.61},
	}
	for _, s := range starts {
		if _, err := db.Exec(`INSERT INTO events (at, kind, run, phase, branch, payload)
			VALUES (?, 'phase_start', ?, ?, ?, '{}')`, s.at, run, s.phase, s.branch); err != nil {
			t.Fatal(err)
		}
	}

	got := phaseStarts(run, "implrulefix", "implementing")
	for branch, want := range map[string]int{"claim-8": 1, "claim-9": 3, "claim-10": 3} {
		if got[branch] != want {
			t.Errorf("%s: %d implrulefix start(s) counted, want %d (all: %v)", branch, got[branch], want, got)
		}
	}

	over := lapsOver(run, map[string]float64{"implrulefix": 2}, "implementing")
	stopped := map[string]bool{}
	for _, b := range over {
		stopped[b.Branch] = true
	}
	if stopped["claim-8"] {
		t.Errorf("claim-8 stopped on its fresh attempt's first repair: %v", over)
	}
	for _, branch := range []string{"claim-9", "claim-10"} {
		if !stopped[branch] {
			t.Errorf("%s ran 3 laps against a cap of 2 and was not stopped: %v", branch, over)
		}
	}
	if len(over) != 2 {
		t.Errorf("wanted claim-9 and claim-10 over the cap, got %v", over)
	}
}
