package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The knowledge base in words, asked in words.
//
// The rows in kb.go answer a question only when the asker already knows the
// topic: "ifp.demand-forecasting.shadow-record" finds how that flag switches
// on, "where do the shadow records go and who may write there" finds nothing.
// Planning asks the second kind of question - from a ticket's words, before it
// knows any topic - so the same base also keeps documents, cut into chunks,
// each with an embedding, and /kb/ask returns the chunks nearest the question
// with the citation each one came from.
//
// The embedding model is whatever AMMIT_EMBED_URL serves (Ollama's /api/embed;
// AMMIT_EMBED_MODEL, nomic-embed-text by default), ranked together with
// FTS5 by reciprocal rank (kbFuse). It runs next to ammit: a
// project's knowledge is its environments, identities and tables, and none of
// that is sent to anybody's API. Without a reachable model the same endpoint
// ranks by words through FTS5 and says so in "backend" and "note" - a question
// answered worse is still answered, and an answer that hides how it was found
// would be read as the good kind.
//
// Every structured entry (kb.go) is indexed here too, as a document whose
// topic is "entry:<topic>", so asking in words finds the flag rows as well.

const kbDocsSchema = `
CREATE TABLE IF NOT EXISTS kb_docs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    project    TEXT NOT NULL,
    topic      TEXT NOT NULL,
    kind       TEXT NOT NULL,
    text       TEXT NOT NULL,
    cite       TEXT NOT NULL,
    learned_by TEXT,
    at         REAL NOT NULL,
    stale      INTEGER NOT NULL DEFAULT 0,
    stale_why  TEXT,
    UNIQUE (project, topic)
);
CREATE TABLE IF NOT EXISTS kb_chunks (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    doc     INTEGER NOT NULL REFERENCES kb_docs(id) ON DELETE CASCADE,
    project TEXT NOT NULL,
    seq     INTEGER NOT NULL,
    text    TEXT NOT NULL,
    model   TEXT,          -- the model the vector came from; NULL: not embedded yet
    vec     BLOB           -- little-endian float32, unit length
);
CREATE INDEX IF NOT EXISTS kb_chunks_project ON kb_chunks (project);
CREATE INDEX IF NOT EXISTS kb_chunks_doc ON kb_chunks (doc);`

// kbDocKinds: the structured kinds, plus what planning learns in prose -
// test data, tables and who may write them, how a run is prepared - and
// "note" for anything else a run found worth keeping.
var kbDocKinds = map[string]bool{"feature_switch": true, "environment": true, "dependency": true,
	"simulator": true, "test_data": true, "table": true, "permission": true, "envprep": true, "note": true}

// kbChunkChars is the size a document is cut at. nomic-embed-text reads 2048
// tokens; a chunk about a paragraph long keeps one fact per vector, so the
// nearest chunk is the fact rather than a page that mentions it.
const kbChunkChars = 900

var kbFTS bool

func openKBDocs() error {
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, kbDocsSchema); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `CREATE VIRTUAL TABLE IF NOT EXISTS kb_fts USING fts5(text, chunk UNINDEXED, project UNINDEXED)`); err != nil {
		log.Printf("ammit: no fts5 for the knowledge base (%v) - /kb/ask without a model answers from LIKE", err)
		return nil
	}
	kbFTS = true
	return nil
}

// kbChunks cuts text at paragraph, then line, then word boundaries.
func kbChunks(text string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	for _, para := range strings.Split(text, "\n\n") {
		if cur.Len()+len(para) > kbChunkChars {
			flush()
		}
		for len(para) > kbChunkChars {
			cut := strings.LastIndexAny(para[:kbChunkChars], "\n ")
			if cut <= 0 {
				cut = kbChunkChars
			}
			cur.WriteString(para[:cut])
			flush()
			para = para[cut:]
		}
		if cur.Len() > 0 {
			cur.WriteString("\n\n")
		}
		cur.WriteString(para)
	}
	flush()
	return out
}

func kbEmbedConfig() (url, model string) {
	url = strings.TrimRight(strings.TrimSpace(os.Getenv("AMMIT_EMBED_URL")), "/")
	model = strings.TrimSpace(os.Getenv("AMMIT_EMBED_MODEL"))
	if model == "" {
		model = "nomic-embed-text"
	}
	return url, model
}

// kbEmbedder: one vector per input, or an error. query says whether the
// inputs are questions or documents - nomic-embed-text was trained with a task
// prefix on each and ranks a document below unrelated ones without it (a test
// data note lost to four permission notes for "where does the test data live").
var kbEmbedder = ollamaEmbed

// kbPrefixes are the task prefixes per model family: (query, document).
var kbPrefixes = map[string][2]string{"nomic-embed-text": {"search_query: ", "search_document: "}}

func kbPrefix(model string, query bool) string {
	p := kbPrefixes[strings.SplitN(model, ":", 2)[0]]
	if query {
		return p[0]
	}
	return p[1]
}

var errNoEmbedder = errors.New("AMMIT_EMBED_URL is not set")

func ollamaEmbed(ctx context.Context, inputs []string, query bool) ([][]float32, string, error) {
	url, model := kbEmbedConfig()
	if url == "" {
		return nil, model, errNoEmbedder
	}
	prefixed := make([]string, len(inputs))
	for i, in := range inputs {
		prefixed[i] = kbPrefix(model, query) + in
	}
	body, _ := json.Marshal(map[string]any{"model": model, "input": prefixed})
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url+"/api/embed", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, model, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
		Error      string      `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, model, err
	}
	if resp.StatusCode != http.StatusOK || out.Error != "" {
		return nil, model, fmt.Errorf("embed %s: %d %s", model, resp.StatusCode, out.Error)
	}
	if len(out.Embeddings) != len(inputs) {
		return nil, model, fmt.Errorf("embed %s: %d vectors for %d inputs", model, len(out.Embeddings), len(inputs))
	}
	for _, v := range out.Embeddings {
		kbNormalize(v)
	}
	// The stored model name says the prefixes were used, so vectors made
	// without them are re-embedded rather than compared.
	if kbPrefix(model, true) != "" {
		model += "+task-prefix"
	}
	return out.Embeddings, model, nil
}

func kbNormalize(v []float32) {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	if s == 0 {
		return
	}
	n := float32(1 / math.Sqrt(s))
	for i := range v {
		v[i] *= n
	}
}

func kbVecBlob(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func kbBlobVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}

func kbDot(a, b []float32) float64 {
	if len(a) != len(b) {
		return -2
	}
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

type kbDoc struct {
	Project   string `json:"project"`
	Topic     string `json:"topic"`
	Kind      string `json:"kind"`
	Text      string `json:"text"`
	Cite      string `json:"cite"`
	LearnedBy string `json:"learned_by,omitempty"`
}

// kbEmbedded is a document cut and embedded, before any lock is taken: the
// model can take seconds and the database mutex is everybody's.
type kbEmbedded struct {
	parts []string
	vecs  [][]float32
	model string
	err   error
}

func embedKBDoc(ctx context.Context, d kbDoc) kbEmbedded {
	e := kbEmbedded{parts: kbChunks(d.Text)}
	if len(e.parts) == 0 {
		e.err = errors.New("text is empty")
		return e
	}
	e.vecs, e.model, e.err = kbEmbedder(ctx, e.parts, false)
	if e.err != nil {
		log.Printf("ammit: kb doc %s/%s stored without vectors: %v", d.Project, d.Topic, e.err)
	}
	return e
}

// storeKBDoc replaces a document and its chunks, with their vectors when the
// model answered. A model that did not answer leaves the chunks without
// vectors; the next ask embeds them (kbBackfill), so nothing is lost to an
// outage. The caller holds the database mutex.
func storeKBDoc(ctx context.Context, d kbDoc, e kbEmbedded) (chunks int, embedded bool, err error) {
	parts, vecs, model, eerr := e.parts, e.vecs, e.model, e.err
	if len(parts) == 0 {
		return 0, false, errors.New("text is empty")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var old sql.NullInt64
	_ = tx.QueryRowContext(ctx, `SELECT id FROM kb_docs WHERE project = ? AND topic = ?`, d.Project, d.Topic).Scan(&old)
	if old.Valid {
		if kbFTS {
			if _, err := tx.ExecContext(ctx, `DELETE FROM kb_fts WHERE chunk IN (SELECT id FROM kb_chunks WHERE doc = ?)`, old.Int64); err != nil {
				return 0, false, err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM kb_chunks WHERE doc = ?`, old.Int64); err != nil {
			return 0, false, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM kb_docs WHERE id = ?`, old.Int64); err != nil {
			return 0, false, err
		}
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO kb_docs (project, topic, kind, text, cite, learned_by, at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, d.Project, d.Topic, d.Kind, d.Text, d.Cite, d.LearnedBy, kbNow())
	if err != nil {
		return 0, false, err
	}
	doc, _ := res.LastInsertId()
	for i, p := range parts {
		var m any
		var blob any
		if eerr == nil {
			m, blob = model, kbVecBlob(vecs[i])
		}
		r, err := tx.ExecContext(ctx, `INSERT INTO kb_chunks (doc, project, seq, text, model, vec) VALUES (?, ?, ?, ?, ?, ?)`,
			doc, d.Project, i, p, m, blob)
		if err != nil {
			return 0, false, err
		}
		if kbFTS {
			id, _ := r.LastInsertId()
			if _, err := tx.ExecContext(ctx, `INSERT INTO kb_fts (text, chunk, project) VALUES (?, ?, ?)`, p, id, d.Project); err != nil {
				return 0, false, err
			}
		}
	}
	return len(parts), eerr == nil, tx.Commit()
}

// kbEntryText is a structured entry as words: what it is, and its body flattened
// to "path: value" lines, so "who may write the shadow table" finds a body
// whose keys say write and table.
func kbEntryText(e kbEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", strings.ReplaceAll(e.Kind, "_", " "), e.Topic)
	var body any
	_ = json.Unmarshal(e.Body, &body)
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch t := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(strings.TrimPrefix(prefix+" "+k, " "), t[k])
			}
		case []any:
			for _, x := range t {
				walk(prefix, x)
			}
		default:
			fmt.Fprintf(&b, "%s: %v\n", prefix, t)
		}
	}
	walk("", body)
	return b.String()
}

// kbEntryCite is the first citation a structured body carries, or the topic.
func kbEntryCite(e kbEntry) string {
	var body map[string]any
	_ = json.Unmarshal(e.Body, &body)
	for _, k := range []string{"citations", "citation", "cite", "source"} {
		switch v := body[k].(type) {
		case string:
			if v != "" {
				return v
			}
		case []any:
			if len(v) == 0 {
				continue
			}
			// A citation is a string, or {source, ref} as the envplan phase writes it.
			if m, ok := v[0].(map[string]any); ok {
				if ref, _ := m["ref"].(string); ref != "" {
					if src, _ := m["source"].(string); src != "" {
						return src + ": " + ref
					}
					return ref
				}
				b, _ := json.Marshal(m)
				return string(b)
			}
			return fmt.Sprint(v[0])
		}
	}
	return "kb:" + e.Project + "/" + e.Topic
}

func putKBDoc(mu *sync.Mutex, w http.ResponseWriter, r *http.Request) {
	var d kbDoc
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not json"})
		return
	}
	d.Project, d.Topic, d.Cite = strings.TrimSpace(d.Project), strings.TrimSpace(d.Topic), strings.TrimSpace(d.Cite)
	d.Kind = strings.ToLower(strings.TrimSpace(d.Kind))
	if d.Project == "" || d.Topic == "" || strings.TrimSpace(d.Text) == "" || !kbDocKinds[d.Kind] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "need project, topic, text and kind (" +
			strings.Join(kbSortedKinds(), ", ") + ")"})
		return
	}
	// What cannot be pointed at cannot be checked: the planner cites what it
	// was told, and "the knowledge base said so" is not a source.
	if d.Cite == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "need cite: the file, ticket, PR or probe this came from"})
		return
	}
	e := embedKBDoc(r.Context(), d)
	mu.Lock()
	defer mu.Unlock()
	n, embedded, err := storeKBDoc(r.Context(), d, e)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "project": d.Project, "topic": d.Topic,
		"chunks": n, "embedded": embedded})
}

// kbEach runs a query and hands every row to each; the rows are closed and
// their error returned, whatever each did.
func kbEach(ctx context.Context, each func(*sql.Rows), query string, args ...any) error {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		each(rows)
	}
	return rows.Err()
}

func kbSortedKinds() []string {
	out := make([]string, 0, len(kbDocKinds))
	for k := range kbDocKinds {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// kbBackfill embeds chunks stored while no model answered, and chunks a
// different model embedded - their vectors are not comparable with the
// question's. Bounded per call: an ask is a question, not a migration. Reads
// and writes under the mutex, asks the model outside it.
func kbBackfill(ctx context.Context, mu *sync.Mutex, project, model string) {
	mu.Lock()
	defer mu.Unlock()
	var ids []int64
	var texts []string
	err := kbEach(ctx, func(rows *sql.Rows) {
		var id int64
		var t string
		if rows.Scan(&id, &t) == nil {
			ids, texts = append(ids, id), append(texts, t)
		}
	}, `SELECT id, text FROM kb_chunks WHERE project = ? AND (vec IS NULL OR model IS NOT ?) LIMIT 64`, project, model)
	if err != nil || len(ids) == 0 {
		return
	}
	mu.Unlock()
	vecs, model, err := kbEmbedder(ctx, texts, false)
	mu.Lock()
	if err != nil {
		return
	}
	for i, id := range ids {
		_, _ = db.ExecContext(ctx, `UPDATE kb_chunks SET model = ?, vec = ? WHERE id = ?`, model, kbVecBlob(vecs[i]), id)
	}
}

type kbHit struct {
	Score     float64 `json:"score"`
	Topic     string  `json:"topic"`
	Kind      string  `json:"kind"`
	Text      string  `json:"text"`
	Cite      string  `json:"cite"`
	LearnedBy string  `json:"learned_by,omitempty"`
	Stale     bool    `json:"stale"`
	StaleWhy  string  `json:"stale_why,omitempty"`
}

const kbHitCols = `c.text, d.topic, d.kind, d.cite, coalesce(d.learned_by, ''), d.stale, coalesce(d.stale_why, '')`

func scanKBHit(sc interface{ Scan(...any) error }, extra ...any) (kbHit, error) {
	var h kbHit
	var stale int
	err := sc.Scan(append([]any{&h.Text, &h.Topic, &h.Kind, &h.Cite, &h.LearnedBy, &stale, &h.StaleWhy}, extra...)...)
	h.Stale = stale != 0
	return h, err
}

// askKB answers {project, q, k} (POST) or ?project=&q=&k= (GET).
func askKB(mu *sync.Mutex, w http.ResponseWriter, r *http.Request) {
	var in struct {
		Project string `json:"project"`
		Q       string `json:"q"`
		K       int    `json:"k"`
	}
	if r.Method == http.MethodPost {
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not json"})
			return
		}
	} else {
		q := r.URL.Query()
		in.Project, in.Q = q.Get("project"), q.Get("q")
		in.K, _ = strconv.Atoi(q.Get("k"))
	}
	in.Project, in.Q = strings.TrimSpace(in.Project), strings.TrimSpace(in.Q)
	if in.Project == "" || in.Q == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "need project and q"})
		return
	}
	if in.K <= 0 || in.K > 50 {
		in.K = 8
	}
	ctx := r.Context()
	qv, model, eerr := kbEmbedder(ctx, []string{in.Q}, true)
	if eerr == nil {
		kbBackfill(ctx, mu, in.Project, model)
	}
	mu.Lock()
	defer mu.Unlock()
	if eerr == nil {
		var hits []kbHit
		err := kbEach(ctx, func(rows *sql.Rows) {
			var blob []byte
			if h, err := scanKBHit(rows, &blob); err == nil {
				h.Score = math.Round(kbDot(qv[0], kbBlobVec(blob))*10000) / 10000
				hits = append(hits, h)
			}
		}, `SELECT `+kbHitCols+`, c.vec FROM kb_chunks c JOIN kb_docs d ON d.id = c.doc
			WHERE c.project = ? AND c.model = ?`, in.Project, model)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
		backend := "vector"
		// Meaning and words, fused by rank. With a handful of notes all about
		// one table, nomic put four permission notes above the one test-data
		// note for "where does the test data live" - a question whose words
		// name it exactly. Each list votes 1/(60+rank); the score returned is
		// the fused one, and each hit still carries its cite.
		if words, _, werr := kbWordHits(ctx, in.Project, in.Q, 4*in.K); werr == nil && len(words) > 0 {
			hits, backend = kbFuse(hits, words), "vector+fts5"
		}
		if len(hits) > in.K {
			hits = hits[:in.K]
		}
		if hits == nil {
			hits = []kbHit{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"project": in.Project, "q": in.Q, "backend": backend,
			"model": model, "hits": hits})
		return
	}
	note := "no embedding model reachable (" + eerr.Error() + "): ranked by words, not meaning"
	hits, backend, err := kbWordHits(ctx, in.Project, in.Q, in.K)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": in.Project, "q": in.Q, "backend": backend,
		"note": note, "hits": hits})
}

// kbFuseK is reciprocal rank fusion's constant: large enough that the first
// place of one list does not drown the second list's.
const kbFuseK = 60

// kbFuse merges two ranked lists of chunks by reciprocal rank.
func kbFuse(lists ...[]kbHit) []kbHit {
	type acc struct {
		hit   kbHit
		score float64
	}
	by := map[string]*acc{}
	var order []string
	for _, l := range lists {
		for rank, h := range l {
			key := h.Topic + "\x00" + h.Text
			a, ok := by[key]
			if !ok {
				a = &acc{hit: h}
				by[key] = a
				order = append(order, key)
			}
			a.score += 1 / float64(kbFuseK+rank+1)
		}
	}
	out := make([]kbHit, 0, len(order))
	for _, k := range order {
		h := by[k].hit
		h.Score = math.Round(by[k].score*100000) / 100000
		out = append(out, h)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// kbWordHits ranks by bm25 over the question's words, any of them matching.
func kbWordHits(ctx context.Context, project, q string, k int) ([]kbHit, string, error) {
	var words []string
	for _, f := range strings.FieldsFunc(strings.ToLower(q), func(c rune) bool {
		isWord := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c > 127
		return !isWord
	}) {
		if len(f) > 2 {
			words = append(words, `"`+f+`"`)
		}
	}
	hits := []kbHit{}
	if len(words) == 0 {
		return hits, "fts5", nil
	}
	if !kbFTS {
		err := kbEach(ctx, func(rows *sql.Rows) {
			if h, err := scanKBHit(rows); err == nil {
				hits = append(hits, h)
			}
		}, `SELECT `+kbHitCols+` FROM kb_chunks c JOIN kb_docs d ON d.id = c.doc
			WHERE c.project = ? AND c.text LIKE ? LIMIT ?`, project, "%"+strings.Trim(words[0], `"`)+"%", k)
		return hits, "like", err
	}
	err := kbEach(ctx, func(rows *sql.Rows) {
		var rank float64
		if h, err := scanKBHit(rows, &rank); err == nil {
			h.Score = math.Round(-rank*10000) / 10000
			hits = append(hits, h)
		}
	}, `SELECT `+kbHitCols+`, bm25(kb_fts) FROM kb_fts
		JOIN kb_chunks c ON c.id = kb_fts.chunk JOIN kb_docs d ON d.id = c.doc
		WHERE kb_fts MATCH ? AND kb_fts.project = ? ORDER BY bm25(kb_fts) LIMIT ?`,
		strings.Join(words, " OR "), project, k)
	return hits, "fts5", err
}

// staleKBDoc marks a document whose content a run found false.
func staleKBDoc(ctx context.Context, project, topic, why string) int64 {
	res, err := db.ExecContext(ctx, `UPDATE kb_docs SET stale = 1, stale_why = ? WHERE project = ? AND topic = ?`,
		why, project, topic)
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}

// getKBDocs lists ?project= documents (no chunks), stale ones marked.
func getKBDocs(mu *sync.Mutex, w http.ResponseWriter, r *http.Request) {
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	if project == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "need project"})
		return
	}
	mu.Lock()
	defer mu.Unlock()
	out := []map[string]any{}
	err := kbEach(r.Context(), func(rows *sql.Rows) {
		var topic, kind, cite, by, why string
		var at float64
		var stale, chars, chunks, embedded int
		if rows.Scan(&topic, &kind, &cite, &by, &at, &stale, &why, &chars, &chunks, &embedded) == nil {
			out = append(out, map[string]any{"topic": topic, "kind": kind, "cite": cite, "learned_by": by, "at": at,
				"stale": stale != 0, "stale_why": why, "chars": chars, "chunks": chunks, "embedded": embedded})
		}
	}, `SELECT d.topic, d.kind, d.cite, coalesce(d.learned_by, ''), d.at, d.stale,
		coalesce(d.stale_why, ''), length(d.text), (SELECT count(*) FROM kb_chunks c WHERE c.doc = d.id),
		(SELECT count(*) FROM kb_chunks c WHERE c.doc = d.id AND c.vec IS NOT NULL)
		FROM kb_docs d WHERE d.project = ? ORDER BY d.kind, d.topic`, project)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": project, "docs": out})
}
