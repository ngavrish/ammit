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
	clear, why := true, ""
	switch status.String {
	case "running":
		clear, why = false, "a deploy is running: the stack is being replaced"
	case "failure":
		clear, why = false, "the last deploy failed: the containers and the "+
			"catalog on disk may be from different commits"
	case "cancelled":
		clear, why = false, "the last deploy was cancelled partway"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"known": true, "clear": clear, "why": why,
		"at": at, "run": run.String, "status": status.String,
		"detail": detail.String, "shas": shas.String,
		"age_seconds": time.Since(time.Unix(int64(at), 0)).Seconds(),
	})
}
