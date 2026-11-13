package main

import "testing"

// Run 362a2ae2 (APF-3313) was stopped as "waiting" while it was queued for the
// working tree behind e9b2695c (APF-3499), a run that was getting on with it
// (WD-66d7f44a-83). The note the runner said every five minutes names the
// holder; the watchdog judges the holder by it, so it has to read it right.
func TestTreeHolderOf(t *testing.T) {
	cases := []struct{ in, want string }{
		{"waiting for the working tree (65m) — APF-3499 has it", "APF-3499"},
		{"waiting for the working tree (5m) — WD-66d7f44a-83 has it", "WD-66d7f44a-83"},
		// Names nobody: there is no holder to judge, so the queue is judged.
		{"waiting for the working tree (5m) — another ticket has it", ""},
		{"waiting for approval (65m)", ""},
		{"waiting for the working tree (5m)", ""},
		{"claim ledger: nothing recorded", ""},
		{"waiting for the working tree (5m) — APF 1 has it", ""},
	}
	for _, c := range cases {
		if got := treeHolderOf(c.in); got != c.want {
			t.Errorf("treeHolderOf(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
