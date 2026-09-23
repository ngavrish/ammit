package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Traceability: what a run undertook to cover, where it came from, and what
// proves it.
//
// The chain existed already and was unaskable. A requirement is recovered from
// the tracker with the paragraphs it was read from and the sentence it quotes;
// a functional requirement is derived from it by a technique and says which one
// it covers; a claim is what that asserts, and one claim is one Scenario
// Outline. All three lived as JSON files in a run directory that the next run
// of the same ticket resets - so "which tests prove this requirement, and how
// do I know that is what the ticket asked for" could be answered for the run in
// front of you and for no other.
//
// Rows are keyed on (run, kind, rid) and written again as often as a phase
// likes: a retry after a timeout rewrites what it wrote, it does not add a
// second copy.

type coverageRow struct {
	Kind   string   `json:"kind"`
	ID     string   `json:"id"`
	Covers []string `json:"covers,omitempty"`
	Source []string `json:"source,omitempty"`
	Quote  string   `json:"quote,omitempty"`
	Title  string   `json:"title,omitempty"`
	Tests  []string `json:"tests,omitempty"`
	// Body is the row as its phase wrote it, kept whole: the columns above are
	// the ones a question is asked by, not the ones a row has.
	Body json.RawMessage `json:"body,omitempty"`
}

type coverageIn struct {
	Run    string        `json:"run"`
	Ticket string        `json:"ticket"`
	Rows   []coverageRow `json:"rows"`
}

// coverageKinds is the closed list. A kind outside it is a typo, and a typo
// that lands is a row nobody will find again.
var coverageKinds = map[string]bool{
	"requirement": true, "funcreq": true, "claim": true,
}

func putCoverage(db *sql.DB, mu *sync.Mutex, w http.ResponseWriter, r *http.Request) {
	var in coverageIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not json"})
		return
	}
	if len(in.Rows) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no rows"})
		return
	}
	var refused []string
	written := 0
	at := float64(time.Now().UnixNano()) / 1e9
	mu.Lock()
	defer mu.Unlock()
	for _, row := range in.Rows {
		kind := strings.TrimSpace(strings.ToLower(row.Kind))
		rid := strings.TrimSpace(row.ID)
		if !coverageKinds[kind] || rid == "" {
			refused = append(refused, fmt.Sprintf("%s/%s", row.Kind, row.ID))
			continue
		}
		// Written again rather than added again: a phase that retries after a
		// timeout is the ordinary case, not the exception.
		_, err := db.ExecContext(r.Context(), `
			INSERT INTO coverage (at, run, ticket, kind, rid, covers, source, quote, title, tests, body)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT (run, kind, rid) DO UPDATE SET
			  at=excluded.at, ticket=excluded.ticket, covers=excluded.covers,
			  source=excluded.source, quote=excluded.quote, title=excluded.title,
			  tests=CASE WHEN excluded.tests <> '' THEN excluded.tests ELSE coverage.tests END,
			  body=excluded.body`,
			at, in.Run, in.Ticket, kind, rid, strings.Join(row.Covers, " "),
			strings.Join(row.Source, " "), row.Quote, row.Title,
			strings.Join(row.Tests, " "), string(row.Body))
		if err != nil {
			refused = append(refused, fmt.Sprintf("%s/%s: %v", kind, rid, err))
			continue
		}
		written++
	}
	sort.Strings(refused)
	reply := map[string]any{"written": written}
	if len(refused) > 0 {
		reply["refused"] = refused
	}
	writeJSON(w, http.StatusOK, reply)
}

// getCoverage answers the traceability question for one ticket or one run:
// every row, with what it covers and what proves it.
func getCoverage(db *sql.DB, mu *sync.Mutex, w http.ResponseWriter, r *http.Request) {
	ticket := strings.TrimSpace(r.URL.Query().Get("ticket"))
	run := strings.TrimSpace(r.URL.Query().Get("run"))
	// Three whole statements rather than one with a clause pasted into it.
	// The clause was one of two fixed strings and never held a caller's text,
	// but a query built by concatenation reads the same whether that is true
	// or not - and the next person to add a filter is the one it catches.
	const cols = `SELECT at, run, ticket, kind, rid, covers, source,
	                     quote, title, tests, body FROM coverage `
	var (
		rows *sql.Rows
		err  error
	)
	mu.Lock()
	switch {
	case run != "":
		rows, err = db.QueryContext(r.Context(), cols+`WHERE run = ? ORDER BY kind, rid`, run)
	case ticket != "":
		rows, err = db.QueryContext(r.Context(), cols+`WHERE ticket = ? ORDER BY kind, rid`, ticket)
	default:
		rows, err = db.QueryContext(r.Context(), cols+`ORDER BY kind, rid`)
	}
	mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer func() { _ = rows.Close() }()
	out := []map[string]any{}
	for rows.Next() {
		var at float64
		var run, ticket, kind, rid, covers, source, quote, title, tests, body sql.NullString
		var atv float64
		if err := rows.Scan(&atv, &run, &ticket, &kind, &rid, &covers, &source,
			&quote, &title, &tests, &body); err != nil {
			continue
		}
		at = atv
		row := map[string]any{
			"at": at, "run": run.String, "ticket": ticket.String,
			"kind": kind.String, "id": rid.String,
			"covers": strings.Fields(covers.String),
			"quote":  quote.String, "title": title.String,
			"source": strings.Fields(source.String),
			"tests":  strings.Fields(tests.String),
		}
		if body.String != "" {
			row["body"] = json.RawMessage(body.String)
		}
		out = append(out, row)
	}
	// A cursor that stopped because the read failed looks exactly like one
	// that stopped because there was nothing left; the difference is here.
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": out})
}
