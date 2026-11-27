package main

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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

//go:embed heal.json
var healSpec []byte

//go:embed model.json
var modelSpec []byte

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
	// Why the query is written the way it is, for whoever changes it next.
	// Not drawn. JSON has nowhere else to keep a reason, and a reason that is
	// only in a commit message is not where the next reader is looking.
	Why string `json:"why,omitempty"`
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
	if localJSON, err := os.ReadFile(chartsLocalPath); err == nil {
		_ = json.Unmarshal(localJSON, &l)
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
	upper := strings.ToUpper(strings.TrimSpace(q))
	return strings.HasPrefix(upper, "SELECT") || strings.HasPrefix(upper, "WITH")
}

// merged is the one spec both /charts/panels and /charts/data read, so an
// index into it means the same panel to both. Built-ins first, additions after
// — hiding marks rather than removes, precisely so the numbering never moves.
// Additions belong to the run pages unless they say scope: lifetime or
// scope: heal or scope: model; those pages are different questions (one row
// per run, no window; the heal loop and the model, about every run at once).
func merged(page string) chartSpec {
	base := panelsOf()
	if pageSpec := pageOf(page); pageSpec != nil && pageSpec.AllTime {
		base = pageSpec.spec()
	}
	loc := localOf()
	away := map[string]bool{}
	for _, hidden := range loc.Hidden {
		away[hidden] = true
	}
	for index := range base.Panels {
		base.Panels[index].Hidden = away[base.Panels[index].Title]
	}
	for _, panel := range loc.Panels {
		if panel.Scope != page {
			continue
		}
		panel.Custom = true
		panel.Hidden = away[panel.Title]
		base.Panels = append(base.Panels, panel)
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

func healOf() chartSpec {
	var s chartSpec
	_ = json.Unmarshal(healSpec, &s)
	return s
}

func modelOf() chartSpec {
	var s chartSpec
	_ = json.Unmarshal(modelSpec, &s)
	return s
}

// The pages that are about every run at once rather than a window of one.
// which set a request is about. The lifetime panels carry no window — they are
// about every run there has been — so the macros are filled with the widest
// possible range rather than being left in the SQL to fail.
func specFor(r *http.Request) (chartSpec, string) {
	page := r.URL.Query().Get("scope")
	if pageSpec := pageOf(page); pageSpec == nil || !pageSpec.AllTime {
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
	if fromParam := r.URL.Query().Get("from"); fromParam != "" {
		if seconds, err := strconv.ParseFloat(fromParam, 64); err == nil {
			from = int64(seconds)
		}
	}
	if toParam := r.URL.Query().Get("to"); toParam != "" {
		if seconds, err := strconv.ParseFloat(toParam, 64); err == nil {
			to = int64(seconds)
		}
	}
	return from, to
}

func fill(sql string, from, to int64) string {
	sql = fillAnswers(sql)
	sql = strings.ReplaceAll(sql, "$__from", strconv.FormatInt(from, 10))
	return strings.ReplaceAll(sql, "$__to", strconv.FormatInt(to, 10))
}

// Every table a panel reads that belongs to a run. limits is not one of them:
// it is the config as it stood, and it stood the same for everybody.
var runScoped = []string{"events", "runs", "gates", "judgements", "calls", "turns", "suites", "heal_laps", "results"}

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
var runIDChars = regexp.MustCompile(`^[0-9a-zA-Z-]+$`)

func scope(sql, run string) string {
	if run == "" {
		return sql
	}
	// One run, or a run and the runs it continued from, comma-separated: a
	// resumed run's result stands on the runs before it, so a page about that
	// result is about all of them. Only run-id characters survive.
	var ids []string
	for _, runID := range strings.Split(run, ",") {
		runID = strings.TrimSpace(runID)
		if runID != "" && runIDChars.MatchString(runID) {
			ids = append(ids, "'"+runID+"'")
		}
	}
	if len(ids) == 0 {
		ids = []string{"''"}
	}
	idList := strings.Join(ids, ",")
	var parts []string
	for _, table := range runScoped {
		where := fmt.Sprintf("run IN (%s)", idList)
		if table == "events" {
			// What the learning side reports - lessons stored, the hoard, shadow
			// evals - belongs to a ticket, not a run, and arrives with no run.
			// A run page keeps its ticket's; the window keeps them to its time.
			where += fmt.Sprintf(" OR (run = '' AND kind = 'learning' AND payload->>'ticket' IN (SELECT name FROM public.runs WHERE run IN (%s)))", idList)
		}
		parts = append(parts, fmt.Sprintf("%s AS (SELECT * FROM public.%s WHERE %s)", table, table, where))
	}
	return "WITH " + strings.Join(parts, ",\n     ") + "\n" + sql
}

// The longest any one question may hold this service.
//
// mu below is taken for the whole of db.Query, and SQLite computes an
// aggregate or a join before it returns its first row - so a single expensive
// query holds the lock the judge loop, the event ingest and every other panel
// need. One did: a self-join on json_extract(payload, '$.request'), a join on
// a computed value that SQLite cannot index, pinned this service at 100% CPU
// for forty minutes with a twelve-branch fan-out running unwatched behind it.
// Closing the HTTP connection did nothing; nothing was cancelling the query.
//
// A question that cannot be answered in a minute is a question asked wrong.
const _QUERY_DEADLINE = 60 * time.Second

// rows runs one query and returns its columns and rows, whatever shape it has.
// Reporting the error as data rather than as a status: a panel that cannot run
// says so in its own box, and the other forty-two still draw.
func rows(query string) map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), _QUERY_DEADLINE)
	defer cancel()
	// Not under mu: mu serialises the writer, and this is the read-only
	// pool, which runs beside it. Holding mu here stalled every POST /events
	// for as long as one slow panel or /query ran - 59 s behind a runaway
	// recursive CTE.
	var cols []string
	table := [][]any{}
	err := readOnly(ctx, query, func(rs *sql.Rows) error {
		cols, _ = rs.Columns()
		types := columnTypes(rs)
		for rs.Next() {
			cells, err := scanCells(rs, types)
			if err != nil {
				continue
			}
			table = append(table, cells)
			if len(table) >= 20000 {
				break
			}
		}
		_ = rs.Err() // a cut-off read is the shorter answer it always was
		return nil
	})
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return map[string]any{"columns": cols, "rows": table}
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
		for _, panel := range l.Panels {
			if strings.TrimSpace(panel.Title) == "" {
				http.Error(w, "a panel needs a title", http.StatusBadRequest)
				return
			}
			// A row is a section heading: it groups the panels under it and
			// asks the database nothing.
			if len(panel.Queries) == 0 && panel.Kind != "row" {
				http.Error(w, "a panel needs a query", http.StatusBadRequest)
				return
			}
			for _, query := range panel.Queries {
				if !readsOnly(query) {
					http.Error(w, "a chart only reads: the query must start with SELECT or WITH",
						http.StatusBadRequest)
					return
				}
			}
		}
		layout, _ := json.MarshalIndent(l, "", " ")
		if err := os.WriteFile(chartsLocalPath, append(layout, '\n'), 0o644); err != nil {
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
		from, until := window(r)
		if page != "" {
			from, until = 0, time.Now().UnixMilli()+86400000
		}
		var series []any
		run := r.URL.Query().Get("run")
		for _, query := range spec.Panels[idx].Queries {
			series = append(series, rows(scope(fill(query, from, until), run)))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"panel": spec.Panels[idx], "from": from, "to": until, "series": series,
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
		filters := r.URL.Query()
		if runName := strings.TrimSpace(filters.Get("name")); runName != "" {
			where = append(where, "coalesce(name,'') ILIKE ?")
			args = append(args, "%"+runName+"%")
		}
		for _, filter := range []struct{ param, col, op string }{
			{"started_from", "started", ">="},
			{"started_to", "started", "<="},
			{"finished_from", "finished", ">="},
			{"finished_to", "finished", "<="},
		} {
			param := strings.TrimSpace(filters.Get(filter.param))
			if param == "" {
				continue
			}
			bound, err := strconv.ParseFloat(param, 64)
			if err != nil {
				continue
			}
			// A finish filter means finished runs. An unfinished run has no
			// finish to compare, and treating its absence as zero would put every
			// run still going into "finished before yesterday".
			where = append(where, fmt.Sprintf("%s IS NOT NULL AND %s %s ?",
				filter.col, filter.col, filter.op))
			args = append(args, bound)
		}
		mu.Lock()
		runRows, err := db.Query(`SELECT run, coalesce(name,''), started,
		                     coalesce(finished,0), coalesce(verdict,''),
		                     coalesce(usd,0), coalesce(turns,0)
		                     FROM runs WHERE `+strings.Join(where, " AND ")+`
		                     ORDER BY started DESC NULLS LAST LIMIT 500`, args...)
		type row struct {
			Run      string  `json:"run"`
			Name     string  `json:"name"`
			Started  float64 `json:"started"`
			Finished float64 `json:"finished"`
			Verdict  string  `json:"verdict"`
			USD      float64 `json:"usd"`
			Turns    int     `json:"turns"`
		}
		runs := []row{}
		if err == nil {
			for runRows.Next() {
				var x row
				if runRows.Scan(&x.Run, &x.Name, &x.Started, &x.Finished, &x.Verdict,
					&x.USD, &x.Turns) == nil {
					runs = append(runs, x)
				}
			}
			_ = runRows.Err() // a cut-off read is the shorter answer it always was
			runRows.Close()
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(runs)
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
		for _, page := range pages {
			if strings.HasPrefix(r.URL.Path, page.Path) {
				active = page.Key
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, chartsPageHTML(active))
	}
	for _, chartPage := range pages {
		mux.HandleFunc("GET "+chartPage.Path, page)
	}
	mux.HandleFunc("GET /ammit/runs/{id}", page)
	mux.HandleFunc("GET /ammit/learning", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ammit/heal", http.StatusFound) })

	// Where the old addresses went, so anything already sent still arrives.
	for from, target := range map[string]string{
		"/charts": "/ammit/runs", "/ammit": "/ammit/runs",
	} {
		where := target
		mux.HandleFunc("GET "+from, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, where, http.StatusFound)
		})
	}
}
