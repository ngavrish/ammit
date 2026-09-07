package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

// Two runs of the same ticket, side by side, in the four numbers that decide
// whether a change to the pipeline was worth making.
//
// It kept happening: a prompt was rewritten, a phase was split, a model was
// swapped, and the argument about whether it helped was had over two terminal
// windows of scrollback. Two runs were compared on turns and money once and
// the conclusion was wrong, because the flows differed and nothing put them in
// the same table.
//
// So: calls by kind, turns, tokens, dollars, wall time and idle time, per
// phase or per agent, for both runs, with the difference already worked out.
// Nothing here is a new measurement - every number comes out of calls, turns,
// the event spans and runs, which have held them all along. What was missing
// was the subtraction.

// idleGap is the silence that counts as idle. Same 120 seconds the charts use:
// longer than any gap a working run leaves between events, shorter than any
// pause worth calling a stall.
const idleGap = 120

type cmpRow struct {
	Search, Read, Write, Test, Map, Cli, Other int64
	Calls                                      int64
	Turns                                      int64
	TokensIn, TokensOut                        int64
	USD                                        float64
	Wall, Idle                                 float64

	// firstAt and lastAt back the wall-time fallback for a group whose span
	// never closed, and are not reported themselves.
	firstAt, lastAt float64
	sawSpan         bool
}

type metricDef struct {
	key, head string
	get       func(*cmpRow) float64
	unit      string // count | tokens | usd | seconds
}

var cmpMetrics = []metricDef{
	{"search", "search", func(r *cmpRow) float64 { return float64(r.Search) }, "count"},
	{"read", "read", func(r *cmpRow) float64 { return float64(r.Read) }, "count"},
	{"write", "write", func(r *cmpRow) float64 { return float64(r.Write) }, "count"},
	{"test", "test", func(r *cmpRow) float64 { return float64(r.Test) }, "count"},
	{"map", "map", func(r *cmpRow) float64 { return float64(r.Map) }, "count"},
	{"cli", "cli", func(r *cmpRow) float64 { return float64(r.Cli) }, "count"},
	{"other", "other", func(r *cmpRow) float64 { return float64(r.Other) }, "count"},
	{"calls", "calls", func(r *cmpRow) float64 { return float64(r.Calls) }, "count"},
	{"turns", "turns", func(r *cmpRow) float64 { return float64(r.Turns) }, "count"},
	{"tokens_in", "tok_in", func(r *cmpRow) float64 { return float64(r.TokensIn) }, "tokens"},
	{"tokens_out", "tok_out", func(r *cmpRow) float64 { return float64(r.TokensOut) }, "tokens"},
	{"usd", "usd", func(r *cmpRow) float64 { return r.USD }, "usd"},
	{"wall_seconds", "wall_s", func(r *cmpRow) float64 { return r.Wall }, "seconds"},
	{"idle_seconds", "idle_s", func(r *cmpRow) float64 { return r.Idle }, "seconds"},
}

func (r *cmpRow) addCall(kind string, n int64) {
	switch kind {
	case "search":
		r.Search += n
	case "read":
		r.Read += n
	case "write":
		r.Write += n
	case "test":
		r.Test += n
	case "map":
		r.Map += n
	case "cli":
		r.Cli += n
	default:
		r.Other += n
	}
	r.Calls += n
}

type runRef struct {
	Run      string  `json:"run"`
	Name     string  `json:"name"`
	Started  float64 `json:"started"`
	Finished float64 `json:"finished"`
	Verdict  string  `json:"verdict"`
	Found    bool    `json:"-"`
}

// resolveRun takes either the run id or the ticket it was run for. A person
// asks for APF-1934 and means the newest one; a query asks for the uuid and
// means exactly that row.
func resolveRun(want string) runRef {
	var r runRef
	var name, verdict any
	var finished any
	mu.Lock()
	defer mu.Unlock()
	err := db.QueryRow(`SELECT run, name, coalesce(started,0), finished, verdict
	                    FROM runs WHERE run=?`, want).
		Scan(&r.Run, &name, &r.Started, &finished, &verdict)
	if err != nil {
		err = db.QueryRow(`SELECT run, name, coalesce(started,0), finished, verdict
		                   FROM runs WHERE name=? ORDER BY started DESC LIMIT 1`, want).
			Scan(&r.Run, &name, &r.Started, &finished, &verdict)
	}
	if err != nil {
		return r
	}
	r.Found = true
	if s, ok := name.(string); ok {
		r.Name = s
	}
	if s, ok := verdict.(string); ok {
		r.Verdict = s
	}
	if f, ok := finished.(float64); ok {
		r.Finished = f
	}
	return r
}

// gather reads one run's numbers out of the tables that already hold them,
// grouped by phase or by agent.
//
// Grouped by agent rather than by session id: a session id is a fresh twelve
// hex characters every time, so two runs share none of them, and "the coder"
// is the thing a comparison is actually about.
func gather(run, by string) (map[string]*cmpRow, *cmpRow) {
	col := "phase"
	if by == "agent" {
		col = "agent"
	}
	rows := map[string]*cmpRow{}
	at := func(key string) *cmpRow {
		if r, ok := rows[key]; ok {
			return r
		}
		r := &cmpRow{}
		rows[key] = r
		return r
	}
	total := &cmpRow{}
	// What the bills said, for the totals row, under the same rule as a row:
	// the turn's own counts win, and the bill fills in only where the turns
	// carried none.
	var billedIn, billedOut float64

	mu.Lock()
	defer mu.Unlock()

	// Calls, by what they were.
	scan(`SELECT coalesce(`+col+`,''), coalesce(kind,'other'), count(*)
	      FROM calls WHERE run=? GROUP BY 1,2`, []any{run}, func(get func(...any) bool) {
		var key, kind string
		var n int64
		if get(&key, &kind, &n) {
			at(key).addCall(kind, n)
			total.addCall(kind, n)
		}
	})

	// Turns and what a turn was sent and returned. tokens_out is the SDK's
	// count and out_est the runner's measure of the same message; the larger
	// is the honest one, and taking it here is why this is not a sum of two
	// units.
	scan(`SELECT coalesce(`+col+`,''), count(*),
	      sum(coalesce(tokens_in,0)),
	      sum(max(coalesce(tokens_out,0), coalesce(out_est,0)))
	      FROM turns WHERE run=? GROUP BY 1`, []any{run}, func(get func(...any) bool) {
		var key string
		var n, in, out int64
		if get(&key, &n, &in, &out) {
			r := at(key)
			r.Turns, r.TokensIn, r.TokensOut = n, in, out
			total.Turns += n
			total.TokensIn += in
			total.TokensOut += out
		}
	})

	// Money, and tokens for a pipeline that reports them on the bill rather
	// than on the turn. Not added to the turn's own counts: a client that
	// sends both is sending the same tokens twice, so the turn wins and the
	// bill fills in only where the turn said nothing.
	scan(`SELECT coalesce(`+col+`,''),
	      sum(coalesce(json_extract(payload,'$.usd'),0)),
	      sum(coalesce(json_extract(payload,'$.tokens_in'),0)),
	      sum(coalesce(json_extract(payload,'$.tokens_out'),0))
	      FROM events WHERE run=? AND kind='spend' GROUP BY 1`, []any{run},
		func(get func(...any) bool) {
			var key string
			var usd, in, out float64
			if get(&key, &usd, &in, &out) {
				r := at(key)
				r.USD += usd
				billedIn += in
				billedOut += out
				if r.TokensIn == 0 {
					r.TokensIn = int64(in)
				}
				if r.TokensOut == 0 {
					r.TokensOut = int64(out)
				}
			}
		})
	if total.TokensIn == 0 {
		total.TokensIn = int64(billedIn)
	}
	if total.TokensOut == 0 {
		total.TokensOut = int64(billedOut)
	}

	// Wall time from the span that closed: a phase_end and a session_end each
	// carry the seconds their span took, so a fan-out of seven sessions is
	// seven session-lengths rather than one wall clock, which is the number
	// worth comparing per agent.
	span := "phase_end"
	if by == "agent" {
		span = "session_end"
	}
	scan(`SELECT coalesce(`+col+`,''), sum(coalesce(json_extract(payload,'$.seconds'),0))
	      FROM events WHERE run=? AND kind=? GROUP BY 1`, []any{run, span},
		func(get func(...any) bool) {
			var key string
			var secs float64
			if get(&key, &secs) {
				r := at(key)
				r.Wall, r.sawSpan = secs, true
			}
		})

	// Idle: every gap longer than idleGap between two things this group said.
	// The same definition the charts draw, so "the phase was quiet for eleven
	// minutes" means one thing in this service and not two.
	scan(`SELECT g, sum(CASE WHEN gap > `+fmt.Sprint(idleGap)+` THEN gap ELSE 0 END),
	      min(at), max(at) FROM (
	        SELECT coalesce(`+col+`,'') AS g, at,
	               at - lag(at) OVER (PARTITION BY coalesce(`+col+`,'') ORDER BY at) AS gap
	        FROM events WHERE run=? AND ifnull(`+col+`,'') <> ''
	      ) GROUP BY g`, []any{run}, func(get func(...any) bool) {
		var key string
		var idle, first, last float64
		if get(&key, &idle, &first, &last) {
			r := at(key)
			r.Idle, r.firstAt, r.lastAt = idle, first, last
		}
	})

	// A group whose span never closed still took the time it took: from its
	// first word to its last. A run killed mid-phase would otherwise compare
	// as a phase of length zero, which reads as the fastest run there has
	// ever been.
	for _, r := range rows {
		if !r.sawSpan && r.lastAt > r.firstAt {
			r.Wall = r.lastAt - r.firstAt
		}
	}

	// The totals row is the run, not the sum of the rows above it. Work
	// reported without a phase - or by no agent - is in the run and in none of
	// them, and a totals line that hid that would be the one number here that
	// cannot be checked against anything.
	var started, finished, usd float64
	var lastEvent float64
	_ = db.QueryRow(`SELECT coalesce(started,0), coalesce(finished,0), coalesce(usd,0)
	                 FROM runs WHERE run=?`, run).Scan(&started, &finished, &usd)
	_ = db.QueryRow(`SELECT coalesce(max(at),0) FROM events WHERE run=?`, run).Scan(&lastEvent)
	total.USD = usd
	if finished == 0 {
		finished = lastEvent
	}
	if finished > started && started > 0 {
		total.Wall = finished - started
	}
	_ = db.QueryRow(`SELECT coalesce(sum(CASE WHEN gap > `+fmt.Sprint(idleGap)+`
	             THEN gap ELSE 0 END),0) FROM (
	               SELECT at - lag(at) OVER (ORDER BY at) AS gap
	               FROM events WHERE run=?)`, run).Scan(&total.Idle)
	return rows, total
}

// scan runs a query and hands each row to fn through a getter that scans it.
// A failed query leaves the numbers it would have filled at zero: half a
// comparison beats an error page, and a zero row is visible as one.
func scan(query string, args []any, fn func(get func(...any) bool)) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		fn(func(into ...any) bool { return rows.Scan(into...) == nil })
	}
}

func metricsJSON(r *cmpRow) map[string]any {
	out := map[string]any{}
	for _, m := range cmpMetrics {
		v := m.get(r)
		if m.unit == "usd" {
			out[m.key] = round(v, 4)
			continue
		}
		if m.unit == "seconds" {
			out[m.key] = round(v, 1)
			continue
		}
		out[m.key] = int64(v)
	}
	return out
}

// deltaJSON is b minus a, and the same as a share of a. A percentage against
// nothing is not a large percentage, it is not a percentage: a row that was
// zero and is now eleven reports null and lets the reader see the eleven.
func deltaJSON(a, b *cmpRow) map[string]any {
	out := map[string]any{}
	for _, m := range cmpMetrics {
		av, bv := m.get(a), m.get(b)
		cell := map[string]any{"abs": round(bv-av, 4), "pct": nil}
		if av != 0 {
			cell["pct"] = round((bv-av)/av*100, 1)
		}
		out[m.key] = cell
	}
	return out
}

func round(v float64, places int) float64 {
	f := 1.0
	for i := 0; i < places; i++ {
		f *= 10
	}
	return float64(int64(v*f+sign(v)*0.5)) / f
}

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

func serveCompare(mux *http.ServeMux) {
	mux.HandleFunc("GET /compare", func(w http.ResponseWriter, r *http.Request) {
		wantA, wantB := r.URL.Query().Get("a"), r.URL.Query().Get("b")
		if wantA == "" || wantB == "" {
			writeJSON(w, http.StatusBadRequest,
				map[string]string{"error": "a and b are required: two runs, by id or by name"})
			return
		}
		by := r.URL.Query().Get("by")
		if by != "agent" {
			by = "phase"
		}
		refA, refB := resolveRun(wantA), resolveRun(wantB)
		for want, ref := range map[string]runRef{wantA: refA, wantB: refB} {
			if !ref.Found {
				writeJSON(w, http.StatusNotFound,
					map[string]string{"error": "no run " + want})
				return
			}
		}
		rowsA, totalA := gather(refA.Run, by)
		rowsB, totalB := gather(refB.Run, by)

		keys := map[string]bool{}
		for k := range rowsA {
			keys[k] = true
		}
		for k := range rowsB {
			keys[k] = true
		}
		ordered := make([]string, 0, len(keys))
		for k := range keys {
			ordered = append(ordered, k)
		}
		sort.Strings(ordered)

		out := []map[string]any{}
		for _, key := range ordered {
			a, b := rowsA[key], rowsB[key]
			if a == nil {
				a = &cmpRow{}
			}
			if b == nil {
				b = &cmpRow{}
			}
			name := key
			if name == "" {
				name = "(none)"
			}
			out = append(out, map[string]any{
				"key": name, "a": metricsJSON(a), "b": metricsJSON(b),
				"delta": deltaJSON(a, b),
			})
		}
		totals := map[string]any{
			"key": "TOTAL", "a": metricsJSON(totalA), "b": metricsJSON(totalB),
			"delta": deltaJSON(totalA, totalB),
		}
		if strings.Contains(r.Header.Get("Accept"), "text/plain") {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = fmt.Fprint(w, compareTable(by, refA, refB, ordered, rowsA, rowsB, totalA, totalB))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"by": by, "a": refA, "b": refB, "rows": out, "totals": totals,
			"idle_gap_seconds": idleGap,
		})
	})
}

// compareTable is the same answer for a terminal: one block per phase or
// agent, four lines - a, b, the difference and the difference as a share -
// under one set of headings.
func compareTable(by string, a, b runRef, keys []string,
	rowsA, rowsB map[string]*cmpRow, totalA, totalB *cmpRow) string {
	var s strings.Builder
	fmt.Fprintf(&s, "by %s   a=%s (%s)   b=%s (%s)\n\n", by,
		a.Run, orNone(a.Name), b.Run, orNone(b.Name))

	const label = 18
	head := strings.Repeat(" ", label) + "   "
	for _, m := range cmpMetrics {
		head += fmt.Sprintf("%9s", m.head)
	}
	s.WriteString(head + "\n")

	block := func(key string, ra, rb *cmpRow) {
		if ra == nil {
			ra = &cmpRow{}
		}
		if rb == nil {
			rb = &cmpRow{}
		}
		name := key
		if name == "" {
			name = "(none)"
		}
		if len(name) > label {
			name = name[:label]
		}
		lines := []struct {
			tag string
			get func(m metricDef) string
		}{
			{"a", func(m metricDef) string { return cell(m, m.get(ra)) }},
			{"b", func(m metricDef) string { return cell(m, m.get(rb)) }},
			{"Δ", func(m metricDef) string { return signed(m, m.get(rb)-m.get(ra)) }},
			{"%", func(m metricDef) string { return pctCell(m.get(ra), m.get(rb)) }},
		}
		for i, line := range lines {
			shown := ""
			if i == 0 {
				shown = name
			}
			row := fmt.Sprintf("%-*s %-2s", label, shown, line.tag)
			for _, m := range cmpMetrics {
				row += fmt.Sprintf("%9s", line.get(m))
			}
			s.WriteString(row + "\n")
		}
	}
	for _, key := range keys {
		block(key, rowsA[key], rowsB[key])
	}
	s.WriteString(strings.Repeat("-", label+3+9*len(cmpMetrics)) + "\n")
	block("TOTAL", totalA, totalB)
	return s.String()
}

func orNone(s string) string {
	if s == "" {
		return "unnamed"
	}
	return s
}

func cell(m metricDef, v float64) string {
	switch m.unit {
	case "usd":
		return fmt.Sprintf("%.2f", v)
	case "seconds":
		return fmt.Sprintf("%.1f", v)
	case "tokens":
		return short(v)
	}
	return fmt.Sprintf("%.0f", v)
}

// signed is the difference with its sign, and a plain 0 when the difference
// rounds away. A column reading "-0.0" is a column that has already cost
// somebody a minute working out whether it means anything.
func signed(m metricDef, v float64) string {
	body := cell(m, v)
	if allZero(body) {
		return "0"
	}
	if v > 0 && !strings.HasPrefix(body, "+") {
		return "+" + body
	}
	return body
}

func allZero(s string) bool {
	for _, r := range s {
		if r >= '1' && r <= '9' {
			return false
		}
	}
	return true
}

func pctCell(a, b float64) string {
	if a == 0 {
		if b == 0 {
			return "0"
		}
		return "new"
	}
	p := (b - a) / a * 100
	if p > 0 {
		return fmt.Sprintf("+%.1f", p)
	}
	return fmt.Sprintf("%.1f", p)
}

// short keeps a token count inside a column: 1.2M reads, 1234567 does not.
func short(v float64) string {
	neg := ""
	if v < 0 {
		neg, v = "-", -v
	}
	switch {
	case v >= 1_000_000:
		return fmt.Sprintf("%s%.1fM", neg, v/1_000_000)
	case v >= 10_000:
		return fmt.Sprintf("%s%.0fk", neg, v/1000)
	}
	return fmt.Sprintf("%s%.0f", neg, v)
}
