package main

import (
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// A deploy that stood aside for a live run is waiting, and a run started now
// makes it stand aside again. On 2026-10-21 eleven deploys in a row yielded to
// APF-3301's back-to-back runs, each new run starting thirty seconds after the
// last ended, and the watchdog cycle that had merged ammit#103 (run 03405ee1,
// cycle 1) failed as "no deploy reported success after the merge".
func TestAFreshYieldIsNotClear(t *testing.T) {
	cases := []struct {
		status string
		age    float64
		clear  bool
	}{
		{"success", 10, true},
		{"yielded", 816, false},           // GET /deploy at 00:08 on 2026-10-22
		{"yielded", yieldHold + 60, true}, // a deploy job that stopped asking
		{"running", 10, false},
		{"failure", 10, false},
		{"cancelled", 10, false},
	}
	for _, c := range cases {
		got, why := deployClear(c.status, c.age)
		if got != c.clear {
			t.Errorf("deployClear(%q, %v) = %v (%q), want %v", c.status, c.age, got, why, c.clear)
		}
		if !got && why == "" {
			t.Errorf("deployClear(%q, %v) refuses without saying why", c.status, c.age)
		}
	}
}

func TestGetDeployAfterAYieldIsNotClear(t *testing.T) {
	if err := openDB(t.TempDir() + "/ammit.db"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO deploys (at, run, status, detail, shas) VALUES (?,?,?,?,?)`,
		float64(time.Now().Add(-13*time.Minute).UnixNano())/1e9, "39696361433", "yielded", "", ""); err != nil {
		t.Fatal(err)
	}
	var lock sync.Mutex
	w := httptest.NewRecorder()
	getDeploy(db, &lock, w, httptest.NewRequest("GET", "/deploy", nil))
	var out struct {
		Clear  bool   `json:"clear"`
		Status string `json:"status"`
		Why    string `json:"why"`
	}
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Clear || out.Status != "yielded" || out.Why == "" {
		t.Fatalf("GET /deploy after a yield 13 minutes ago = %+v, want not clear with a reason", out)
	}
}

// The queue does not start into a waiting deploy: the item stays waiting, and
// goes once the deploy has reported success.
func TestQueueWaitsForAYieldedDeploy(t *testing.T) {
	if err := openDB(t.TempDir() + "/ammit.db"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO queue (name, payload, requested) VALUES (?,?,?)`,
		"WD-03405ee1-2", "{}", 1792625488.2559104); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO deploys (at, run, status, detail, shas) VALUES (?,?,?,?,?)`,
		float64(time.Now().Add(-2*time.Minute).UnixNano())/1e9, "39696361433", "yielded", "", ""); err != nil {
		t.Fatal(err)
	}
	saved := queueStart
	defer func() { queueStart = saved }()
	started := 0
	queueStart = func(_ string, _ Config, _ map[string]string) string {
		started++
		return "started"
	}
	state := func() string {
		t.Helper()
		var s string
		if err := db.QueryRow(`SELECT state FROM queue WHERE name=?`, "WD-03405ee1-2").Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	pumpQueue(Config{})
	if started != 0 || state() != "waiting" {
		t.Fatalf("after a yield: started %d, row %q; want 0 and waiting", started, state())
	}

	if _, err := db.Exec(`INSERT INTO deploys (at, run, status, detail, shas) VALUES (?,?,?,?,?)`,
		float64(time.Now().UnixNano())/1e9, "39696999999", "success", "", ""); err != nil {
		t.Fatal(err)
	}
	pumpQueue(Config{})
	if started != 1 || state() != "running" {
		t.Fatalf("after the deploy succeeded: started %d, row %q; want 1 and running", started, state())
	}
}
