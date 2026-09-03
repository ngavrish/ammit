package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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

// The same pipeline seen from far enough away that one run is a dot: how runs
// end, what one costs, whether that is moving. One row per run rather than one
// point per second, and no window — all of it, always.
//
//go:embed lifetime.json
var lifetimeSpec []byte

//go:embed learning.json
var learningSpec []byte

type panel struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	// What the heading says. The title is the panel's name everywhere else -
	// hiding, kinds, additions - and can be long; the label is short.
	Label string `json:"label,omitempty"`
	// Above every group, first thing on the page: the one table the page is
	// opened for.
	Top bool `json:"top,omitempty"`
	// Columns the page may colour the same rows by, when the query returns
	// them instead of one metric: ["agent", "phase"] is a switch in the heading.
	By []string `json:"by,omitempty"`
	// agg "sum": rows that share a moment and a name are added; acc "cumsum":
	// every line is its own running total. Together they let one row-per-event
	// query stand in for a window-function query per colouring.
	Agg string `json:"agg,omitempty"`
	Acc string `json:"acc,omitempty"`
	// kinds: the kind to draw for a particular colouring, when one of them
	// wants a different shape - {"time": "scatter"} on a candles panel says
	// that over time the same points are points.
	Kinds   map[string]string `json:"kinds,omitempty"`
	About   string            `json:"about"`
	Unit    string            `json:"unit"`
	Height  int               `json:"height"`
	Queries []string          `json:"queries"`
	Hidden  bool              `json:"hidden,omitempty"`
	Custom  bool              `json:"custom,omitempty"`
	// "lifetime" puts an added chart on the all-time page instead of the run
	// pages: a chart of every run has no business on the page of one.
	Scope string `json:"scope,omitempty"`
}

type chartSpec struct {
	// "rows": the page sections by the spec's own rows, in its order, rather
	// than by unit. A page that tells a story keeps its chapters.
	Sections string  `json:"sections,omitempty"`
	Panels   []panel `json:"panels"`
}

// What the person watching added or took away, kept beside limits.yml for the
// same reason the limits are: a chart worth adding during one run is worth
// having on the next machine, and a file in deploy/ is a diff somebody reads
// rather than a browser's local storage nobody can.
type localSpec struct {
	Hidden []string `json:"hidden"`
	Panels []panel  `json:"panels"`
}

var chartsLocalPath = filepath.Join(
	filepath.Dir(env("AMMIT_CONFIG", "/config/limits.yml")), "charts-local.json")

func localOf() localSpec {
	var l localSpec
	if body, err := os.ReadFile(chartsLocalPath); err == nil {
		_ = json.Unmarshal(body, &l)
	}
	if l.Hidden == nil {
		l.Hidden = []string{}
	}
	if l.Panels == nil {
		l.Panels = []panel{}
	}
	return l
}

// A chart is a reader. The page that saves these runs on the same socket that
// edits the limits, so this is not a trust boundary — it is a seatbelt against
// a typo: a query that starts any other way than a SELECT gets refused before
// it is a file, not discovered as a schema change later.
func readsOnly(q string) bool {
	head := strings.ToUpper(strings.TrimSpace(q))
	return strings.HasPrefix(head, "SELECT") || strings.HasPrefix(head, "WITH")
}

// merged is the one spec both /charts/panels and /charts/data read, so an
// index into it means the same panel to both. Built-ins first, additions after
// — hiding marks rather than removes, precisely so the numbering never moves.
// Additions belong to the run pages unless they say scope: lifetime or
// scope: learning; those pages are different questions (one row per run, no
// window; and the learning loop, which is about every run at once).
func merged(page string) chartSpec {
	base := panelsOf()
	switch page {
	case "lifetime":
		base = lifetimeOf()
	case "learning":
		base = learningOf()
	}
	loc := localOf()
	away := map[string]bool{}
	for _, t := range loc.Hidden {
		away[t] = true
	}
	for i := range base.Panels {
		base.Panels[i].Hidden = away[base.Panels[i].Title]
	}
	for _, p := range loc.Panels {
		if p.Scope != page {
			continue
		}
		p.Custom = true
		p.Hidden = away[p.Title]
		base.Panels = append(base.Panels, p)
	}
	return base
}

func panelsOf() chartSpec {
	var s chartSpec
	_ = json.Unmarshal(panelSpec, &s)
	return s
}

func lifetimeOf() chartSpec {
	var s chartSpec
	_ = json.Unmarshal(lifetimeSpec, &s)
	return s
}

func learningOf() chartSpec {
	var s chartSpec
	_ = json.Unmarshal(learningSpec, &s)
	return s
}

// The pages that are about every run at once rather than a window of one.
var allTime = map[string]bool{"lifetime": true, "learning": true}

// which set a request is about. The lifetime panels carry no window — they are
// about every run there has been — so the macros are filled with the widest
// possible range rather than being left in the SQL to fail.
func specFor(r *http.Request) (chartSpec, string) {
	page := r.URL.Query().Get("scope")
	if !allTime[page] {
		page = ""
	}
	return merged(page), page
}

// window turns Grafana's macros into the numbers they stood for. The queries
// were written against $__from and $__to in milliseconds, and rewriting forty-
// three of them to say something else would be work for its own sake.
func window(r *http.Request) (from, to int64) {
	now := time.Now().UnixMilli()
	to = now
	from = now - 12*3600*1000
	// A float is accepted: runs.started is a float of seconds, so the page
	// asking for a run's own window sent 1788377388476.8336, ParseInt refused
	// it, and every run page was silently drawn on the twelve-hour default -
	// the run at the far right edge of a chart of nothing.
	if v := r.URL.Query().Get("from"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			from = int64(n)
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			to = int64(n)
		}
	}
	return from, to
}

func fill(sql string, from, to int64) string {
	sql = strings.ReplaceAll(sql, "$__from", strconv.FormatInt(from, 10))
	return strings.ReplaceAll(sql, "$__to", strconv.FormatInt(to, 10))
}

// Every table a panel reads that belongs to a run. limits is not one of them:
// it is the config as it stood, and it stood the same for everybody.
var runScoped = []string{"events", "runs", "gates", "judgements", "calls"}

// scope narrows a query to one run without touching the query.
//
// Narrowing by time was the first attempt and it is wrong the moment two runs
// overlap — queue.parallel is a number in a file, it is 1 today and can be 2
// tomorrow, and then a run's span holds somebody else's work as well.
//
// So the tables are shadowed instead. SQLite resolves a bare name to the
// common table expression and a schema-qualified one to the real table, so
// each becomes itself filtered to this run, and sixty queries go on saying
// exactly what they said before.
func scope(sql, run string) string {
	if run == "" {
		return sql
	}
	quoted := strings.ReplaceAll(run, "'", "''")
	var parts []string
	for _, t := range runScoped {
		parts = append(parts, fmt.Sprintf("%s AS (SELECT * FROM main.%s WHERE run = '%s')",
			t, t, quoted))
	}
	return "WITH " + strings.Join(parts, ",\n     ") + "\n" + sql
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
		spec, _ := specFor(r)
		json.NewEncoder(w).Encode(spec)
	})

	mux.HandleFunc("GET /charts/local", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(localOf())
	})

	mux.HandleFunc("POST /charts/local", func(w http.ResponseWriter, r *http.Request) {
		var l localSpec
		if err := json.NewDecoder(r.Body).Decode(&l); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for _, p := range l.Panels {
			if strings.TrimSpace(p.Title) == "" {
				http.Error(w, "a panel needs a title", http.StatusBadRequest)
				return
			}
			if len(p.Queries) == 0 {
				http.Error(w, "a panel needs a query", http.StatusBadRequest)
				return
			}
			for _, q := range p.Queries {
				if !readsOnly(q) {
					http.Error(w, "a chart only reads: the query must start with SELECT or WITH",
						http.StatusBadRequest)
					return
				}
			}
		}
		body, _ := json.MarshalIndent(l, "", " ")
		if err := os.WriteFile(chartsLocalPath, append(body, '\n'), 0o644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /charts/data", func(w http.ResponseWriter, r *http.Request) {
		idx, err := strconv.Atoi(r.URL.Query().Get("panel"))
		spec, page := specFor(r)
		if err != nil || idx < 0 || idx >= len(spec.Panels) {
			http.Error(w, "no such panel", http.StatusNotFound)
			return
		}
		from, to := window(r)
		if page != "" {
			from, to = 0, time.Now().UnixMilli()+86400000
		}
		var series []any
		run := r.URL.Query().Get("run")
		for _, q := range spec.Panels[idx].Queries {
			series = append(series, rows(scope(fill(q, from, to), run)))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"panel": spec.Panels[idx], "from": from, "to": to, "series": series,
		})
	})

	// The runs, newest first. Every filter optional and every one absent by
	// default: asked for nothing, this answers with everything there has ever
	// been, which is the question somebody opening the page is usually asking.
	//
	//   ?name=      part of a ticket, matched anywhere in it
	//   ?started_from= / ?started_to=   unix seconds
	//   ?finished_from= / ?finished_to=
	mux.HandleFunc("GET /charts/runs", func(w http.ResponseWriter, r *http.Request) {
		where := []string{"1=1"}
		var args []any
		q := r.URL.Query()
		if v := strings.TrimSpace(q.Get("name")); v != "" {
			where = append(where, "coalesce(name,'') LIKE ?")
			args = append(args, "%"+v+"%")
		}
		for _, f := range []struct{ param, col, op string }{
			{"started_from", "started", ">="},
			{"started_to", "started", "<="},
			{"finished_from", "finished", ">="},
			{"finished_to", "finished", "<="},
		} {
			v := strings.TrimSpace(q.Get(f.param))
			if v == "" {
				continue
			}
			n, err := strconv.ParseFloat(v, 64)
			if err != nil {
				continue
			}
			// A finish filter means finished runs. An unfinished run has no
			// finish to compare, and treating its absence as zero would put every
			// run still going into "finished before yesterday".
			where = append(where, fmt.Sprintf("%s IS NOT NULL AND %s %s ?",
				f.col, f.col, f.op))
			args = append(args, n)
		}
		mu.Lock()
		rs, err := db.Query(`SELECT run, coalesce(name,''), started,
		                     coalesce(finished,0), coalesce(verdict,''),
		                     coalesce(usd,0), coalesce(turns,0)
		                     FROM runs WHERE `+strings.Join(where, " AND ")+`
		                     ORDER BY started DESC LIMIT 500`, args...)
		type row struct {
			Run      string  `json:"run"`
			Name     string  `json:"name"`
			Started  float64 `json:"started"`
			Finished float64 `json:"finished"`
			Verdict  string  `json:"verdict"`
			USD      float64 `json:"usd"`
			Turns    int     `json:"turns"`
		}
		out := []row{}
		if err == nil {
			for rs.Next() {
				var x row
				if rs.Scan(&x.Run, &x.Name, &x.Started, &x.Finished, &x.Verdict,
					&x.USD, &x.Turns) == nil {
					out = append(out, x)
				}
			}
			rs.Close()
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})

	// Every view is a path. /ammit/runs, /ammit/runs/<id>, /ammit/window,
	// /ammit/lifetime — the same page, told by its address which of them it is.
	// A fragment was the first attempt and it is not an address a server can be
	// asked about: it never leaves the browser.
	// Which tab to light, taken from the path. The page re-lights it as you
	// move, but the first paint should not wait for JavaScript to say where you
	// already are.
	page := func(w http.ResponseWriter, r *http.Request) {
		active := "runs"
		switch {
		case strings.HasPrefix(r.URL.Path, "/ammit/window"):
			active = "window"
		case strings.HasPrefix(r.URL.Path, "/ammit/lifetime"):
			active = "lifetime"
		case strings.HasPrefix(r.URL.Path, "/ammit/learning"):
			active = "learning"
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, chartsPageHTML(active))
	}
	mux.HandleFunc("GET /ammit/runs", page)
	mux.HandleFunc("GET /ammit/runs/{id}", page)
	mux.HandleFunc("GET /ammit/window", page)
	mux.HandleFunc("GET /ammit/lifetime", page)
	mux.HandleFunc("GET /ammit/learning", page)

	// Where the old addresses went, so anything already sent still arrives.
	for from, to := range map[string]string{
		"/charts": "/ammit/runs", "/ammit": "/ammit/runs",
	} {
		where := to
		mux.HandleFunc("GET "+from, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, where, http.StatusFound)
		})
	}
}
