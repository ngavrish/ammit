package main

// The project's tickets, when they do not live in Jira: a checklist, a
// spreadsheet, a Google Sheet, a test management system's export. ammit
// keeps them the way it keeps the product - one document, asked for by API:
//
//	GET /tickets          {"tickets": [...], "updated": <unix>}
//	GET /tickets/{key}    one ticket
//	PUT /tickets          replace them all: JSON {"tickets": [...]}, or the
//	                      document itself as text/csv, text/tab-separated-values
//	                      or text/markdown (one ticket per row or list item)
//
// A Jira project never writes here; its tickets stay in Jira.

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Ticket is one unit of work, as much of it as a document can say.
type Ticket struct {
	Key         string `json:"key"`
	Summary     string `json:"summary"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status,omitempty"`
	URL         string `json:"url,omitempty"`
}

type ticketDoc struct {
	Tickets []Ticket `json:"tickets"`
	Updated int64    `json:"updated"`
}

const ticketsMaxBytes = 8 << 20

// Header names a document may use for each field, compared lowercased with
// spaces and punctuation dropped.
var ticketColumns = map[string][]string{
	"key":         {"key", "id", "ticket", "ticketid", "issue", "issuekey", "caseid", "testid", "no", "number", "#"},
	"summary":     {"summary", "title", "name", "case", "testcase", "check", "item", "task", "subject"},
	"description": {"description", "details", "steps", "body", "text", "expected", "expectedresult", "notes", "acceptancecriteria"},
	"status":      {"status", "state", "stage", "column"},
	"url":         {"url", "link", "href"},
}

var notWord = regexp.MustCompile(`[^a-z0-9#]+`)

// keyShape is what a ticket key looks like when a document has one: letters,
// a dash, digits (WEB-12, C1042 is taken as-is too).
var keyShape = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*-?\d+$`)

// parseTickets reads a document of tickets by its media type. A row or item
// with no key of its own is numbered T-1, T-2, ... in document order.
func parseTickets(body []byte, mediaType string) ([]Ticket, error) {
	var out []Ticket
	switch mediaType {
	case "application/json":
		var doc ticketDoc
		if err := json.Unmarshal(body, &doc); err != nil {
			return nil, fmt.Errorf("not a tickets document: %v", err)
		}
		out = doc.Tickets
	case "text/csv", "text/tab-separated-values":
		r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))))
		if mediaType == "text/tab-separated-values" {
			r.Comma = '\t'
		}
		r.FieldsPerRecord = -1
		rows, err := r.ReadAll()
		if err != nil {
			return nil, fmt.Errorf("not a readable table: %v", err)
		}
		if len(rows) < 2 {
			return nil, fmt.Errorf("the table has no rows under its header")
		}
		col := map[string]int{}
		for i, h := range rows[0] {
			name := notWord.ReplaceAllString(strings.ToLower(h), "")
			for field, names := range ticketColumns {
				if _, taken := col[field]; taken {
					continue
				}
				for _, n := range names {
					if name == n {
						col[field] = i
					}
				}
			}
		}
		if _, ok := col["summary"]; !ok {
			return nil, fmt.Errorf("no column says what the ticket is: name one Title, Summary or Name (header was: %s)",
				strings.Join(rows[0], ", "))
		}
		cell := func(row []string, field string) string {
			i, ok := col[field]
			if !ok || i >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[i])
		}
		for _, row := range rows[1:] {
			t := Ticket{Key: cell(row, "key"), Summary: cell(row, "summary"), Description: cell(row, "description"),
				Status: cell(row, "status"), URL: cell(row, "url")}
			if t.Summary == "" && t.Description == "" {
				continue
			}
			out = append(out, t)
		}
	case "text/markdown", "text/plain":
		item := regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+(?:\[( |x|X)\]\s+)?(.+)$`)
		for _, line := range strings.Split(string(body), "\n") {
			m := item.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			t := Ticket{Summary: strings.TrimSpace(m[2])}
			if strings.EqualFold(m[1], "x") {
				t.Status = "Done"
			}
			// "WEB-12: Cart keeps its items" names its own key.
			if k, rest, ok := strings.Cut(t.Summary, ":"); ok && keyShape.MatchString(strings.TrimSpace(k)) {
				t.Key, t.Summary = strings.TrimSpace(k), strings.TrimSpace(rest)
			}
			out = append(out, t)
		}
	default:
		return nil, fmt.Errorf("cannot read %s: send CSV, TSV, Markdown or JSON", mediaType)
	}
	seen := map[string]bool{}
	n := 0
	for i := range out {
		if out[i].Key == "" {
			for {
				n++
				if k := fmt.Sprintf("T-%d", n); !seen[k] {
					out[i].Key = k
					break
				}
			}
		}
		if seen[out[i].Key] {
			return nil, fmt.Errorf("ticket %s appears twice", out[i].Key)
		}
		seen[out[i].Key] = true
		if out[i].Summary == "" {
			out[i].Summary = firstLineOf(out[i].Description)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no tickets found in the document")
	}
	return out, nil
}

func firstLineOf(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

func serveTickets(mux *http.ServeMux, path string) {
	var mu sync.Mutex
	load := func() (ticketDoc, error) {
		var doc ticketDoc
		body, err := os.ReadFile(path)
		if err != nil {
			return doc, err
		}
		return doc, json.Unmarshal(body, &doc)
	}
	mux.HandleFunc("GET /tickets", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		doc, err := load()
		mu.Unlock()
		if errors.Is(err, fs.ErrNotExist) {
			writeJSON(w, http.StatusOK, ticketDoc{Tickets: []Ticket{}})
			return
		}
		if err != nil {
			http.Error(w, "the tickets ammit holds are unreadable: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, doc)
	})
	mux.HandleFunc("GET /tickets/{key}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		doc, err := load()
		mu.Unlock()
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			http.Error(w, "the tickets ammit holds are unreadable: "+err.Error(), http.StatusInternalServerError)
			return
		}
		key := r.PathValue("key")
		for _, t := range doc.Tickets {
			if strings.EqualFold(t.Key, key) {
				writeJSON(w, http.StatusOK, t)
				return
			}
		}
		http.Error(w, "no such ticket: "+key, http.StatusNotFound)
	})
	mux.HandleFunc("PUT /tickets", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, ticketsMaxBytes))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if mt == "" {
			mt = "application/json"
		}
		tickets, err := parseTickets(body, mt)
		if err != nil {
			http.Error(w, "refused: "+err.Error(), http.StatusBadRequest)
			return
		}
		doc := ticketDoc{Tickets: tickets, Updated: time.Now().Unix()}
		out, _ := json.MarshalIndent(doc, "", " ")
		mu.Lock()
		defer mu.Unlock()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.WriteFile(path, out, 0o600); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("ammit: tickets replaced (%d)", len(tickets))
		writeJSON(w, http.StatusOK, map[string]any{"count": len(tickets)})
	})
}
