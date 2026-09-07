package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

// One question asked of everything a run said.
//
// The record has held the words all along and there was no way to ask for
// them: an AttributeError that appeared in one log line of one branch was
// findable by knowing which run, which phase and which endpoint to page
// through, which is another way of saying it was findable by already knowing
// where it was. The documents were worse - a phase's map or requirements are
// files on a disk under a path a row remembers, so the only way to find a word
// inside one was to fetch every one of them and grep.
//
// So one index over both, maintained where they land: the prose fields of an
// event, and the body of a document. What it holds is on RECORD.md with
// everything else, because an index of things nobody listed is a second record
// with no page.

// searchedFields are the event fields that carry prose. A field that holds a
// number or an id is not searched: "3600" matching a timeout, a token count
// and a session id in one answer is an answer nobody can use.
var searchedFields = []string{"text", "note", "summary", "error", "reason", "why", "detail"}

// indexMaxBytes is how much of a document is indexed. A framework map is over
// a megabyte and a run makes several; the whole body stays on disk and is
// served in full by /documents, and what is searchable is the first megabyte.
// Written as a number rather than left unbounded, because an index that grows
// with the largest artefact anybody ever posts is a limit nobody set.
const indexMaxBytes = 1 << 20

// snippetWidth is how much of the match comes back with it. Wide enough for
// the line the word was on, narrow enough that fifty results are a page.
const snippetWidth = 160

// searchFTS says whether this build's sqlite has FTS5. The modernc driver
// ships it, and this is checked rather than assumed: without it the same
// endpoint answers from a LIKE over the same table, which finds substrings and
// cannot rank, and says so in the reply.
var searchFTS bool

const searchSchema = `
-- The searchable text of an event or a document, kept beside the thing it came
-- from rather than in place of it: the event keeps its payload, the document
-- keeps its file, and this holds the words with the id to get the rest.
CREATE TABLE IF NOT EXISTS search_text (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    source  TEXT NOT NULL,      -- event | document
    ref     INTEGER NOT NULL,   -- events.id or documents.id
    run     TEXT,
    kind    TEXT NOT NULL,
    at      REAL NOT NULL,
    phase   TEXT,
    session TEXT,
    body    TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS search_text_ref ON search_text (source, ref);
CREATE INDEX IF NOT EXISTS search_text_run ON search_text (run, kind);`

// openSearch makes the index and finds out whether this build can rank. The
// FTS5 table carries no copy of the text - it points at search_text - so the
// words are stored once.
func openSearch() {
	if _, err := db.Exec(searchSchema); err != nil {
		log.Printf("ammit: no search index (%v)", err)
		return
	}
	if _, err := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS search_fts
	                      USING fts5(body, content='search_text', content_rowid='id')`); err != nil {
		log.Printf("ammit: this sqlite has no fts5 (%v) - /search will use LIKE, "+
			"which finds substrings and cannot rank", err)
		return
	}
	searchFTS = true
}

// searchable is the prose an event carries, joined. Empty for an event that
// said nothing in words, and those are not indexed: a heartbeat is not a
// search result.
func searchable(e event) string {
	parts := make([]string, 0, len(searchedFields)+2)
	for _, f := range searchedFields {
		if v := strings.TrimSpace(e.s(f)); v != "" {
			parts = append(parts, v)
		}
	}
	// A call is what an agent did, and what it did is mostly the command it
	// ran: the tool and the strings it was given. "which session ran this
	// grep" is the question the calls table was added for, and until now it
	// could only be asked of an exact signature.
	if e.s("kind") == "call" {
		if tool := e.s("tool"); tool != "" {
			parts = append(parts, tool)
		}
		if input, ok := e["input"].(map[string]any); ok {
			for _, v := range input {
				if s, is := v.(string); is && strings.TrimSpace(s) != "" {
					parts = append(parts, s)
				}
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// indexEvent files one event's words. Called from store, which holds the lock.
func indexEvent(id int64, at float64, e event) {
	body := searchable(e)
	if body == "" {
		return
	}
	indexRow("event", id, e.s("run"), e.s("kind"), at, e.s("phase"), e.s("session"), body)
}

// indexDocument files a document's body, up to indexMaxBytes.
func indexDocument(id int64, at float64, run, kind, phase, body string) {
	if len(body) > indexMaxBytes {
		body = body[:indexMaxBytes]
		// Cut on a rune boundary: half a character in the index is a token
		// nothing matches and a snippet that renders as a question mark.
		for len(body) > 0 && !utf8.ValidString(body) {
			body = body[:len(body)-1]
		}
	}
	if strings.TrimSpace(body) == "" {
		return
	}
	indexRow("document", id, run, kind, at, phase, "", body)
}

func indexRow(source string, ref int64, run, kind string, at float64,
	phase, session, body string) {
	res, err := db.Exec(`INSERT OR IGNORE INTO search_text
	                     (source, ref, run, kind, at, phase, session, body)
	                     VALUES (?,?,?,?,?,?,?,?)`,
		source, ref, run, kind, at, phase, session, body)
	if err != nil {
		log.Printf("ammit: could not index a %s: %v", source, err)
		return
	}
	if !searchFTS {
		return
	}
	rowid, err := res.LastInsertId()
	if err != nil || rowid == 0 {
		return
	}
	if _, err := db.Exec(`INSERT INTO search_fts (rowid, body) VALUES (?,?)`,
		rowid, body); err != nil {
		log.Printf("ammit: could not index a %s for search: %v", source, err)
	}
}

// indexHistory files everything already kept that has no row yet: the events
// in one statement, the documents by reading the files back off the disk. Runs
// once on start, and picks up where it stopped, so a database that predates
// this index becomes searchable without anybody replaying anything.
func indexHistory() {
	mu.Lock()
	defer mu.Unlock()
	joined := make([]string, 0, len(searchedFields))
	for _, f := range searchedFields {
		joined = append(joined, `coalesce(json_extract(payload,'$.`+f+`'),'')`)
	}
	body := strings.Join(joined, ` || ' ' || `)
	if _, err := db.Exec(`INSERT OR IGNORE INTO search_text
		(source, ref, run, kind, at, phase, session, body)
		SELECT 'event', id, run, kind, at, coalesce(phase,''), coalesce(session,''),
		       trim(` + body + `)
		FROM events
		WHERE id > (SELECT coalesce(max(ref),0) FROM search_text WHERE source='event')
		  AND trim(` + body + `) <> ''`); err != nil {
		log.Printf("ammit: could not index the events already kept: %v", err)
	}

	rows, err := db.Query(`SELECT d.id, d.at, coalesce(d.run,''), d.kind,
	                       coalesce(d.phase,''), d.path FROM documents d
	                       WHERE NOT EXISTS (SELECT 1 FROM search_text s
	                         WHERE s.source='document' AND s.ref=d.id)`)
	if err != nil {
		return
	}
	type doc struct {
		id                     int64
		at                     float64
		run, kind, phase, path string
	}
	var todo []doc
	for rows.Next() {
		var d doc
		if rows.Scan(&d.id, &d.at, &d.run, &d.kind, &d.phase, &d.path) == nil {
			todo = append(todo, d)
		}
	}
	rows.Close()
	for _, d := range todo {
		raw, err := os.ReadFile(d.path)
		if err != nil {
			continue // the volume lost it; the row stays, the words are gone
		}
		indexDocument(d.id, d.at, d.run, d.kind, d.phase, string(raw))
	}
	if len(todo) > 0 {
		log.Printf("ammit: indexed %d document(s) for search", len(todo))
	}
	syncFTS()
}

// syncFTS brings the index up to the table after a bulk insert.
func syncFTS() {
	if !searchFTS {
		return
	}
	if _, err := db.Exec(`INSERT INTO search_fts (rowid, body)
	                      SELECT id, body FROM search_text
	                      WHERE id > (SELECT coalesce(max(rowid),0) FROM search_fts)`); err != nil {
		log.Printf("ammit: could not bring the search index up to date: %v", err)
	}
}

// ftsQuery turns what somebody typed into something FTS5 will accept. Every
// word becomes a quoted term and the terms are ANDed: an AttributeError with a
// colon in it is a search, not a syntax error in a query language the person
// asking never agreed to learn.
func ftsQuery(q string) string {
	var terms []string
	for _, word := range strings.Fields(q) {
		word = strings.ReplaceAll(word, `"`, "")
		if word != "" {
			terms = append(terms, `"`+word+`"`)
		}
	}
	return strings.Join(terms, " ")
}

// snippet is snippetWidth characters of the body with the match inside it,
// centred on the first word of the query. Cut on runes, so a snippet of a
// document in any alphabet is still that alphabet.
func snippet(body, q string) string {
	runes := []rune(body)
	at := 0
	if first := strings.Fields(q); len(first) > 0 {
		if i := strings.Index(strings.ToLower(body), strings.ToLower(first[0])); i >= 0 {
			at = utf8.RuneCountInString(body[:i])
		}
	}
	start := at - snippetWidth/3
	if start < 0 {
		start = 0
	}
	end := start + snippetWidth
	if end > len(runes) {
		end = len(runes)
		start = end - snippetWidth
		if start < 0 {
			start = 0
		}
	}
	out := strings.Join(strings.Fields(string(runes[start:end])), " ")
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}

type hit struct {
	Source  string  `json:"source"`  // event | document
	ID      int64   `json:"id"`      // events.id, or documents.id for a document
	Run     string  `json:"run"`     //
	Kind    string  `json:"kind"`    //
	At      float64 `json:"at"`      //
	Phase   string  `json:"phase"`   //
	Session string  `json:"session"` //
	Snippet string  `json:"snippet"` //
	Fetch   string  `json:"fetch"`   // where the whole of it is
}

func serveSearch(mux *http.ServeMux) {
	mux.HandleFunc("GET /search", func(w http.ResponseWriter, r *http.Request) {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" {
			writeJSON(w, http.StatusBadRequest,
				map[string]string{"error": "q is required"})
			return
		}
		limit := 50
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
				limit = n
			}
		}
		where, args := "", []any{}
		if run := r.URL.Query().Get("run"); run != "" {
			// By id or by ticket, the same as /compare: whoever is searching
			// knows the ticket, not the twelve hex characters.
			ref := resolveRun(run)
			id := run
			if ref.Found {
				id = ref.Run
			}
			where, args = where+" AND s.run=?", append(args, id)
		}
		if kind := r.URL.Query().Get("kind"); kind != "" {
			where, args = where+" AND s.kind=?", append(args, kind)
		}

		var query string
		engine := "like"
		if searchFTS {
			engine = "fts5"
			query = `SELECT s.source, s.ref, coalesce(s.run,''), s.kind, s.at,
			         coalesce(s.phase,''), coalesce(s.session,''), s.body
			         FROM search_fts f JOIN search_text s ON s.id = f.rowid
			         WHERE search_fts MATCH ?` + where + `
			         ORDER BY bm25(search_fts) LIMIT ?`
			args = append([]any{ftsQuery(q)}, args...)
		} else {
			// No FTS5 in this build. Same endpoint, same answer shape, one
			// difference worth naming: LIKE matches substrings rather than
			// words, so "err" finds "error", and there is no ranking - the
			// newest matches come back first.
			query = `SELECT s.source, s.ref, coalesce(s.run,''), s.kind, s.at,
			         coalesce(s.phase,''), coalesce(s.session,''), s.body
			         FROM search_text s
			         WHERE s.body LIKE '%' || ? || '%' ESCAPE '\'` + where + `
			         ORDER BY s.at DESC LIMIT ?`
			args = append([]any{likeTerm(q)}, args...)
		}
		args = append(args, limit)

		out := []hit{}
		mu.Lock()
		rows, err := db.Query(query, args...)
		if err == nil {
			for rows.Next() {
				var h hit
				var body string
				if rows.Scan(&h.Source, &h.ID, &h.Run, &h.Kind, &h.At, &h.Phase,
					&h.Session, &body) != nil {
					continue
				}
				h.Snippet = snippet(body, q)
				h.Fetch = fmt.Sprintf("/query?sql=SELECT+*+FROM+events+WHERE+id=%d", h.ID)
				if h.Source == "document" {
					h.Fetch = fmt.Sprintf("/documents?id=%d", h.ID)
				}
				out = append(out, h)
			}
			rows.Close()
		}
		mu.Unlock()
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if strings.Contains(r.Header.Get("Accept"), "text/plain") {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			for _, h := range out {
				_, _ = fmt.Fprintf(w, "%-8s %-8d %-12s %-14s %-12s %s\n",
					h.Source, h.ID, orNone(h.Run), h.Kind, orNone(h.Phase), h.Snippet)
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"q": q, "engine": engine, "hits": out, "count": len(out),
		})
	})
}

// likeTerm keeps a query's own wildcards out of the LIKE. Somebody searching
// for a path with an underscore in it means the underscore.
func likeTerm(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(q)
}
