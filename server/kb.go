package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// What a product's runs have learned about the product, kept per project.
//
// A new product does not come with an answer to "how is this feature switched
// on, and which environment can a test run it on". APF-2327 (2026-10-09 to
// 12) spent three days finding out that its UAT had no /test-support and no
// readable log while the local stand had both - and the next ticket of the same
// product would have started from nothing again. The runner's
// /runs/_memory/<ticket> is per ticket and a learning event is a sentence; an
// answer the next run can act on is a row with a probe in it.
//
// One row per (project, topic). topic is a flag key, "environments" for the
// product's environment map, "dependency:<name>" for what a test meets when a
// requirement reaches outside the service (real per environment, an approved
// simulator, or nothing at all), or "simulator:<key>" for a simulator the
// product itself ships. kind says which: feature_switch, environment,
// dependency or simulator. body is the answer as the envplan phase wrote it - mechanism
// per environment, how to turn it on and off, the probe that proves it, the
// citation. A run that finds the probe failing marks the row stale; the next
// envplan re-learns it and writes it again, which clears the mark.

const kbSchema = `
CREATE TABLE IF NOT EXISTS kb (
    project    TEXT NOT NULL,
    topic      TEXT NOT NULL,
    kind       TEXT NOT NULL,
    body       TEXT NOT NULL,
    learned_by TEXT,
    at         REAL NOT NULL,
    stale      INTEGER NOT NULL DEFAULT 0,
    stale_why  TEXT,
    stale_by   TEXT,
    stale_at   REAL,
    PRIMARY KEY (project, topic)
);`

// kbKinds is the closed list; a typo that lands is a row nobody asks for.
var kbKinds = map[string]bool{"feature_switch": true, "environment": true,
	"dependency": true, "simulator": true}

// kbSimulatorFields are what a simulator entry must carry. Only a simulator
// the product ships is approved - its compose file, its ci-tests fixtures - and
// the citation is what says so: a simulator nobody can point at in the product
// is a mock somebody invented, and the base refuses to hand it out.
var kbSimulatorFields = []string{"dependency", "simulator", "start", "admin", "probe", "covers", "citations"}

// kbBodyProblem says why a body cannot be stored for its kind; "" when it can.
func kbBodyProblem(kind string, body map[string]any) string {
	switch kind {
	case "simulator":
		for _, f := range kbSimulatorFields {
			v, ok := body[f]
			if !ok || v == nil || v == "" {
				return "a simulator needs " + strings.Join(kbSimulatorFields, ", ") + "; missing " + f
			}
			if l, isList := v.([]any); isList && len(l) == 0 {
				return "a simulator needs " + strings.Join(kbSimulatorFields, ", ") + "; " + f + " is empty"
			}
		}
	case "dependency":
		envs, _ := body["environments"].(map[string]any)
		if len(envs) == 0 {
			return "a dependency needs environments: {env: {kind: real|simulator|unavailable}}"
		}
		for env, raw := range envs {
			e, _ := raw.(map[string]any)
			switch e["kind"] {
			case "real", "unavailable":
			case "simulator":
				if s, _ := e["simulator"].(string); s == "" {
					return "dependency on " + env + ": kind simulator names no simulator"
				}
			default:
				return "dependency on " + env + ": kind must be real, simulator or unavailable"
			}
		}
		if c, _ := body["citations"].([]any); len(c) == 0 {
			return "a dependency needs citations"
		}
	}
	return ""
}

type kbEntry struct {
	Project   string          `json:"project"`
	Topic     string          `json:"topic"`
	Kind      string          `json:"kind"`
	Body      json.RawMessage `json:"body"`
	LearnedBy string          `json:"learned_by,omitempty"`
	At        float64         `json:"at,omitempty"`
	Stale     bool            `json:"stale"`
	StaleWhy  string          `json:"stale_why,omitempty"`
	StaleBy   string          `json:"stale_by,omitempty"`
	StaleAt   float64         `json:"stale_at,omitempty"`
}

func kbNow() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// putKB writes or replaces one entry. Writing an entry again is how a stale one
// is re-learned: the mark goes with the old body.
func putKB(db *sql.DB, mu *sync.Mutex, w http.ResponseWriter, r *http.Request) {
	var in kbEntry
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not json"})
		return
	}
	in.Project, in.Topic = strings.TrimSpace(in.Project), strings.TrimSpace(in.Topic)
	in.Kind = strings.TrimSpace(strings.ToLower(in.Kind))
	if in.Project == "" || in.Topic == "" || !kbKinds[in.Kind] {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "need project, topic and kind (feature_switch, environment, dependency or simulator)"})
		return
	}
	var probe map[string]any
	if json.Unmarshal(in.Body, &probe) != nil || len(probe) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be a JSON object"})
		return
	}
	if why := kbBodyProblem(in.Kind, probe); why != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": why})
		return
	}
	at := kbNow()
	// Indexed in words as well, so a question that does not know the topic
	// finds the entry (kb_docs.go). Embedded before the lock.
	doc := kbDoc{Project: in.Project, Topic: "entry:" + in.Topic, Kind: in.Kind,
		Text: kbEntryText(in), Cite: kbEntryCite(in), LearnedBy: in.LearnedBy}
	emb := embedKBDoc(r.Context(), doc)
	mu.Lock()
	defer mu.Unlock()
	_, err := db.ExecContext(r.Context(), `
		INSERT INTO kb (project, topic, kind, body, learned_by, at, stale, stale_why, stale_by, stale_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, NULL, NULL, NULL)
		ON CONFLICT (project, topic) DO UPDATE SET kind = excluded.kind, body = excluded.body,
			learned_by = excluded.learned_by, at = excluded.at,
			stale = 0, stale_why = NULL, stale_by = NULL, stale_at = NULL`,
		in.Project, in.Topic, in.Kind, string(in.Body), in.LearnedBy, at)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	_, embedded, derr := storeKBDoc(r.Context(), doc, emb)
	if derr != nil {
		log.Printf("ammit: kb entry %s/%s not indexed for /kb/ask: %v", in.Project, in.Topic, derr)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "project": in.Project, "topic": in.Topic, "at": at,
		"indexed": derr == nil, "embedded": embedded})
}

// staleKB marks an entry whose probe failed: {project, topic, why, run}.
func staleKB(db *sql.DB, mu *sync.Mutex, w http.ResponseWriter, r *http.Request) {
	var in struct {
		Project string `json:"project"`
		Topic   string `json:"topic"`
		Why     string `json:"why"`
		Run     string `json:"run"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Project == "" || in.Topic == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "need project and topic"})
		return
	}
	mu.Lock()
	defer mu.Unlock()
	res, err := db.ExecContext(r.Context(), `
		UPDATE kb SET stale = 1, stale_why = ?, stale_by = ?, stale_at = ?
		WHERE project = ? AND topic = ?`, in.Why, in.Run, kbNow(), in.Project, in.Topic)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	n, _ := res.RowsAffected()
	// A document of the same topic, or the entry's own words, go stale with it.
	n += staleKBDoc(r.Context(), in.Project, in.Topic, in.Why)
	n += staleKBDoc(r.Context(), in.Project, "entry:"+in.Topic, in.Why)
	if n == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such entry"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// getKB answers ?project=<p>[&topic=<t>][&kind=<k>]: every entry of the
// project, stale ones included and marked, so a run sees what it must re-learn.
func getKB(db *sql.DB, mu *sync.Mutex, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	project := strings.TrimSpace(q.Get("project"))
	if project == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "need project"})
		return
	}
	topic, kind := strings.TrimSpace(q.Get("topic")), strings.TrimSpace(q.Get("kind"))
	mu.Lock()
	out := []kbEntry{}
	// An empty filter matches every row: one statement, no SQL built from parts.
	err := kbEach(r.Context(), func(rows *sql.Rows) {
		var e kbEntry
		var body string
		var stale int
		if rows.Scan(&e.Project, &e.Topic, &e.Kind, &body, &e.LearnedBy, &e.At, &stale,
			&e.StaleWhy, &e.StaleBy, &e.StaleAt) == nil {
			e.Body, e.Stale = json.RawMessage(body), stale != 0
			out = append(out, e)
		}
	}, `SELECT project, topic, kind, body, coalesce(learned_by, ''), at, stale,
		       coalesce(stale_why, ''), coalesce(stale_by, ''), coalesce(stale_at, 0)
		FROM kb WHERE project = ? AND (? = '' OR topic = ?) AND (? = '' OR kind = ?)
		ORDER BY kind, topic`, project, topic, topic, kind, kind)
	mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": project, "entries": out})
}
