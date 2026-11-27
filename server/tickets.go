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
			return nil, fmt.Errorf("not a tickets document: %w", err)
		}
		out = doc.Tickets
	case "text/csv", "text/tab-separated-values":
		csvReader := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))))
		if mediaType == "text/tab-separated-values" {
			csvReader.Comma = '\t'
		}
		csvReader.FieldsPerRecord = -1
		records, err := csvReader.ReadAll()
		if err != nil {
			return nil, fmt.Errorf("not a readable table: %w", err)
		}
		if len(records) < 2 {
			return nil, fmt.Errorf("the table has no rows under its header")
		}
		col := map[string]int{}
		for column, header := range records[0] {
			heading := notWord.ReplaceAllString(strings.ToLower(header), "")
			for field, names := range ticketColumns {
				if _, taken := col[field]; taken {
					continue
				}
				for _, alias := range names {
					if heading == alias {
						col[field] = column
					}
				}
			}
		}
		if _, ok := col["summary"]; !ok {
			return nil, fmt.Errorf("no column says what the ticket is: name one Title, Summary or Name (header was: %s)",
				strings.Join(records[0], ", "))
		}
		cell := func(row []string, field string) string {
			column, ok := col[field]
			if !ok || column >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[column])
		}
		for _, record := range records[1:] {
			ticket := Ticket{Key: cell(record, "key"), Summary: cell(record, "summary"), Description: cell(record, "description"),
				Status: cell(record, "status"), URL: cell(record, "url")}
			if ticket.Summary == "" && ticket.Description == "" {
				continue
			}
			out = append(out, ticket)
		}
	case "text/markdown", "text/plain":
		listItem := regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+(?:\[( |x|X)\]\s+)?(.+)$`)
		for _, bodyLine := range strings.Split(string(body), "\n") {
			match := listItem.FindStringSubmatch(bodyLine)
			if match == nil {
				continue
			}
			ticket := Ticket{Summary: strings.TrimSpace(match[2])}
			if strings.EqualFold(match[1], "x") {
				ticket.Status = "Done"
			}
			// "WEB-12: Cart keeps its items" names its own key.
			if prefix, summary, ok := strings.Cut(ticket.Summary, ":"); ok && keyShape.MatchString(strings.TrimSpace(prefix)) {
				ticket.Key, ticket.Summary = strings.TrimSpace(prefix), strings.TrimSpace(summary)
			}
			out = append(out, ticket)
		}
	default:
		return nil, fmt.Errorf("cannot read %s: send CSV, TSV, Markdown or JSON", mediaType)
	}
	taken := map[string]bool{}
	counter := 0
	for index := range out {
		if out[index].Key == "" {
			for {
				counter++
				if made := fmt.Sprintf("T-%d", counter); !taken[made] {
					out[index].Key = made
					break
				}
			}
		}
		if taken[out[index].Key] {
			return nil, fmt.Errorf("ticket %s appears twice", out[index].Key)
		}
		taken[out[index].Key] = true
		if out[index].Summary == "" {
			out[index].Summary = firstLineOf(out[index].Description)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no tickets found in the document")
	}
	return out, nil
}

func firstLineOf(s string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return first
}

func serveTickets(mux *http.ServeMux, path string) {
	var mu sync.Mutex
	load := func() (ticketDoc, error) {
		var doc ticketDoc
		stored, err := os.ReadFile(path)
		if err != nil {
			return doc, err
		}
		return doc, json.Unmarshal(stored, &doc)
	}
	mux.HandleFunc("GET /tickets", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		stored, err := load()
		mu.Unlock()
		if errors.Is(err, fs.ErrNotExist) {
			writeJSON(w, http.StatusOK, ticketDoc{Tickets: []Ticket{}})
			return
		}
		if err != nil {
			http.Error(w, "the tickets ammit holds are unreadable: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, stored)
	})
	mux.HandleFunc("GET /tickets/{key}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		stored, err := load()
		mu.Unlock()
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			http.Error(w, "the tickets ammit holds are unreadable: "+err.Error(), http.StatusInternalServerError)
			return
		}
		ticketKey := r.PathValue("key")
		for _, ticket := range stored.Tickets {
			if strings.EqualFold(ticket.Key, ticketKey) {
				writeJSON(w, http.StatusOK, ticket)
				return
			}
		}
		http.Error(w, "no such ticket: "+ticketKey, http.StatusNotFound)
	})
	mux.HandleFunc("PUT /tickets", func(w http.ResponseWriter, r *http.Request) {
		upload, err := io.ReadAll(io.LimitReader(r.Body, ticketsMaxBytes))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if mediaType == "" {
			mediaType = "application/json"
		}
		tickets, err := parseTickets(upload, mediaType)
		if err != nil {
			http.Error(w, "refused: "+err.Error(), http.StatusBadRequest)
			return
		}
		stored := ticketDoc{Tickets: tickets, Updated: time.Now().Unix()}
		encoded, _ := json.MarshalIndent(stored, "", " ")
		mu.Lock()
		defer mu.Unlock()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("ammit: tickets replaced (%d)", len(tickets))
		writeJSON(w, http.StatusOK, map[string]any{"count": len(tickets)})
	})
}
