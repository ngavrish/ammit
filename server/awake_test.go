package main

import (
	"testing"
)

// Run df4cd16a (APF-3122): the VM froze inside a round, after the round guard
// had passed and before weigh read the pulse. On the far side the pulse was
// 1013s old and the uptime 1032s, all but twenty of them slept through, and
// timeouts.heartbeat (120) sent stop_run at a run that had been asleep with
// its host. A pulse aged across our own sleep is not judged; a pulse that goes
// quiet while we watch still is.
func TestHeartbeatIsNotJudgedAcrossASleepInsideTheRound(t *testing.T) {
	if err := openDB(t.TempDir() + "/ammit.db"); err != nil {
		t.Fatal(err)
	}
	const run = "df4cd16a-c85a-49c7-a038-caee39714454"
	conf := Config{"timeouts": {"heartbeat": "120"}}
	now := nowWall()
	if _, err := db.Exec(`INSERT INTO runs (run, name, started) VALUES (?,?,?)`,
		run, "APF-3122", now-5000); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO events (at, kind, run, payload) VALUES (?,?,?,?)`,
		now-1013, "heartbeat", run, `{}`); err != nil {
		t.Fatal(err)
	}
	judged := func() int {
		t.Helper()
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM judgements
		                       WHERE run=? AND rule='timeouts.heartbeat'`, run).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Awake since the dark wake 1032s ago by the round guard; last seen
	// running 1012s ago, the moment the VM froze mid-round.
	awakeMu.Lock()
	_upSince, _seen = now-1032, now-1012
	awakeMu.Unlock()
	weigh(conf)
	if n := judged(); n != 0 {
		t.Fatalf("a pulse aged across our own sleep was judged %d time(s)", n)
	}

	// Watched throughout: the same silence is the runner's, and is judged.
	awakeMu.Lock()
	_upSince, _seen = now-5000, nowWall()
	awakeMu.Unlock()
	weigh(conf)
	if n := judged(); n != 1 {
		t.Fatalf("a pulse silent 1013s while we watched was judged %d time(s), want 1", n)
	}
}
