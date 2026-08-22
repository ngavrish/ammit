package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The charts, drawn by this service rather than by a neighbour.
//
// They were Grafana: six hundred and thirty-one megabytes of image, a plugin
// process, a provisioning tree and a dashboard file that existed in two
// repositories at once and drifted between them — to draw forty-three panels
// from one sqlite file onto a page this service already serves.
//
// What a panel is, underneath: a title, a sentence saying what it is for, a
// kind, and a query returning time, metric and value. The generator that wrote
// the Grafana dashboard now writes that directly, and this reads it.

//go:embed panels.json
var panelSpec []byte

type panel struct {
	Kind    string   `json:"kind"`
	Title   string   `json:"title"`
	About   string   `json:"about"`
	Unit    string   `json:"unit"`
	Height  int      `json:"height"`
	Queries []string `json:"queries"`
}

type chartSpec struct {
	Panels []panel `json:"panels"`
}

func panelsOf() chartSpec {
	var s chartSpec
	_ = json.Unmarshal(panelSpec, &s)
	return s
}

// window turns Grafana's macros into the numbers they stood for. The queries
// were written against $__from and $__to in milliseconds, and rewriting forty-
// three of them to say something else would be work for its own sake.
func window(r *http.Request) (from, to int64) {
	now := time.Now().UnixMilli()
	to = now
	from = now - 12*3600*1000
	if v := r.URL.Query().Get("from"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			from = n
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			to = n
		}
	}
	return from, to
}

func fill(sql string, from, to int64) string {
	sql = strings.ReplaceAll(sql, "$__from", strconv.FormatInt(from, 10))
	return strings.ReplaceAll(sql, "$__to", strconv.FormatInt(to, 10))
}

// rows runs one query and returns its columns and rows, whatever shape it has.
// Reporting the error as data rather than as a status: a panel that cannot run
// says so in its own box, and the other forty-two still draw.
func rows(sql string) map[string]any {
	mu.Lock()
	rs, err := db.Query(sql)
	mu.Unlock()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	defer rs.Close()
	cols, _ := rs.Columns()
	out := [][]any{}
	for rs.Next() {
		cells := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if rs.Scan(ptrs...) != nil {
			continue
		}
		for i, c := range cells {
			if b, ok := c.([]byte); ok {
				cells[i] = string(b)
			}
		}
		out = append(out, cells)
		if len(out) >= 20000 {
			break
		}
	}
	return map[string]any{"columns": cols, "rows": out}
}

func serveCharts(mux *http.ServeMux) {
	mux.HandleFunc("GET /charts/panels", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(panelSpec)
	})

	mux.HandleFunc("GET /charts/data", func(w http.ResponseWriter, r *http.Request) {
		idx, err := strconv.Atoi(r.URL.Query().Get("panel"))
		spec := panelsOf()
		if err != nil || idx < 0 || idx >= len(spec.Panels) {
			http.Error(w, "no such panel", http.StatusNotFound)
			return
		}
		from, to := window(r)
		var series []any
		for _, q := range spec.Panels[idx].Queries {
			series = append(series, rows(fill(q, from, to)))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"panel": spec.Panels[idx], "from": from, "to": to, "series": series,
		})
	})

	mux.HandleFunc("GET /charts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, chartsPage)
	})
}
