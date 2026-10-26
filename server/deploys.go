package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// What the deploy is doing, so a run can decline to start into it.
//
// The lease answers "is a deploy holding the machine right now". It does not
// answer "is one about to", nor "did the last one finish" - and a run started
// in either window runs against a stack that is part old: containers built
// from one commit, a flow catalog read from another, every finding after that
// attributed to the code under test.
//
// The deploy posts here when it starts and again when it ends, whatever the
// end was. A run reads it before dispatch. Nothing here blocks anything: it
// is a fact, and the decision belongs to whoever is about to spend an hour.

// deployStates is the closed list. A status outside it is a typo, and a typo
// that lands reads as "not running" to everyone who asks.
var deployStates = map[string]bool{
	"running": true, "success": true, "failure": true, "cancelled": true,
	"yielded": true, // stood aside for a run that was already going
}

type deployIn struct {
	Run    string `json:"run"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Shas   string `json:"shas"`
}

func putDeploy(db *sql.DB, mu *sync.Mutex, w http.ResponseWriter, r *http.Request) {
	var in deployIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not json"})
		return
	}
	status := strings.TrimSpace(strings.ToLower(in.Status))
	if !deployStates[status] {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "status is running, success, failure, cancelled or yielded"})
		return
	}
	mu.Lock()
	_, err := db.ExecContext(r.Context(),
		`INSERT INTO deploys (at, run, status, detail, shas) VALUES (?,?,?,?,?)`,
		float64(time.Now().UnixNano())/1e9, in.Run, status, in.Detail, in.Shas)
	mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status})
}

// getDeploy answers with the last thing the deploy said, and whether a run
// should start into it.
//
// `clear` is the whole answer for a caller that wants one: the last deploy
// finished and it finished well. A deploy still running, or one that ended
// badly, is not a reason this service refuses anything - it is a reason the
// caller should.
func getDeploy(db *sql.DB, mu *sync.Mutex, w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	row := db.QueryRowContext(r.Context(),
		`SELECT at, run, status, detail, shas FROM deploys ORDER BY at DESC LIMIT 1`)
	var at float64
	var run, status, detail, shas sql.NullString
	err := row.Scan(&at, &run, &status, &detail, &shas)
	mu.Unlock()
	if err == sql.ErrNoRows {
		// Nothing has ever been said. A deployment where the deploy does not
		// report is not one where every run must refuse to start.
		writeJSON(w, http.StatusOK, map[string]any{
			"known": false, "clear": true,
			"why": "no deploy has reported here yet"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	age := time.Since(time.Unix(int64(at), 0)).Seconds()
	clear, why := deployClear(status.String, age)
	writeJSON(w, http.StatusOK, map[string]any{
		"known": true, "clear": clear, "why": why,
		"at": at, "run": run.String, "status": status.String,
		"detail": detail.String, "shas": shas.String,
		"age_seconds": age,
	})
}

// yieldHold is how long a deploy that stood aside for a live run keeps new
// runs from starting. A deploy that yields now waits in its own job and says
// "yielded" every thirty seconds until the run ends (chiron-actions #6), so a
// fresh yield means a deploy is waiting. Two minutes with no word from it is a
// job that has ended, and that holds nothing. It was an hour, on the belief
// that the schedule came back every few minutes: GitHub delivered the next
// tick more than half an hour late, and APF-3122's resume was refused from
// 12:51 on 2026-10-26 with nothing live on the stand.
const yieldHold = 120.0

// deployClear is whether a run may start after the deploy last said status,
// age seconds ago, and why not when it may not.
//
// "yielded" used to be clear, and that starved the deploy: it stands aside for
// a live run, the run ends, and the next one - a retry, a resume, a watchdog
// fix run - starts thirty seconds later, long before the deploy comes back to
// look. On 2026-10-21 eleven deploys in a row yielded, 22:00 to 23:54, to
// APF-3301's back-to-back runs, and the watchdog cycle that had merged
// ammit#103 failed as "no deploy reported success after the merge within
// timeouts.watchdog_deploy" (run 03405ee1, cycle 1).
func deployClear(status string, age float64) (bool, string) {
	switch status {
	case "running":
		return false, "a deploy is running: the stack is being replaced"
	case "failure":
		return false, "the last deploy failed: the containers and the " +
			"catalog on disk may be from different commits"
	case "cancelled":
		return false, "the last deploy was cancelled partway"
	case "yielded":
		if age < yieldHold {
			return false, "a deploy is waiting for the live run to end: a run " +
				"started now would make it stand aside again"
		}
	}
	return true, ""
}

// lastDeployClear is deployClear for the last thing the deploy said; nothing
// said is clear, as it is for GET /deploy. The caller holds mu.
func lastDeployClear() (bool, string) {
	var at float64
	var status string
	if db.QueryRow(`SELECT at, status FROM deploys ORDER BY at DESC LIMIT 1`).
		Scan(&at, &status) != nil {
		return true, ""
	}
	return deployClear(status, time.Since(time.Unix(int64(at), 0)).Seconds())
}
