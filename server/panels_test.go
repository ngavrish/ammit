package main

import (
	"fmt"
	"strings"
	"testing"
)

// Seventy-odd SQL strings and no compiler. An error in one of them used to
// be a red box on the page, found by whoever opened it. This runs every
// query of every page against the schema the service runs, both bare and
// scoped to one run the way the run pages ask, and holds each to the column
// contract the page draws by: a chart wants "time" first and a "value";
// what lies between them names the series. A table may say what it likes.
func TestEveryPanelQueryRuns(t *testing.T) {
	if err := openDB(t.TempDir() + "/ammit.db"); err != nil {
		t.Fatal(err)
	}
	n := 0
	for name, spec := range map[string]chartSpec{
		"runs": panelsOf(), "lifetime": lifetimeOf(), "heal": healOf(), "model": modelOf(),
	} {
		faults, ran := queryFaults(spec)
		n += ran
		for _, f := range faults {
			t.Errorf("%s / %s", name, f)
		}
	}
	if n < 100 {
		t.Fatalf("ran %d queries: the pages have gone missing", n)
	}
}

// The gate, watched refusing: a query that does not parse, and a chart whose
// columns are not the contract, both come back as faults.
func TestAPanelQueryThatIsWrongIsCaught(t *testing.T) {
	if err := openDB(t.TempDir() + "/ammit.db"); err != nil {
		t.Fatal(err)
	}
	bad := chartSpec{Panels: []panel{
		{Kind: "series", Title: "does not parse", Queries: []string{"SELEC at AS time, 'x' AS metric, 1 AS value FROM turns"}},
		{Kind: "bars", Title: "wrong columns", Queries: []string{"SELECT at, run FROM turns"}},
		{Kind: "table", Title: "a table says what it likes", Queries: []string{"SELECT at, run FROM turns"}},
	}}
	// Each query runs twice, bare and run-scoped: two wrong panels, four faults.
	faults, _ := queryFaults(bad)
	if len(faults) != 4 {
		t.Fatalf("wanted the two wrong panels caught twice each and the table let through, got %v", faults)
	}
	for _, want := range []string{"does not parse", "wrong columns"} {
		if !strings.Contains(strings.Join(faults, "\n"), want) {
			t.Errorf("%q was not caught: %v", want, faults)
		}
	}
}

var chartKinds = map[string]bool{"series": true, "scatter": true, "columns": true, "stacked": true,
	"bars": true, "pie": true, "candles": true, "timeline": true}

func queryFaults(spec chartSpec) (faults []string, ran int) {
	for _, p := range spec.Panels {
		if p.Kind == "row" {
			continue
		}
		for i, q := range p.Queries {
			sql := strings.ReplaceAll(strings.ReplaceAll(q, "$__from", "0"), "$__to", "9999999999999")
			for _, s := range []string{sql, scope(sql, "a-run")} {
				ran++
				rows, err := db.Query(s)
				if err != nil {
					faults = append(faults, fmt.Sprintf("%q / query %d: %v", p.Title, i, err))
					continue
				}
				cols, _ := rows.Columns()
				rows.Close()
				if chartKinds[p.Kind] && (len(cols) < 3 || cols[0] != "time" || !has(cols, "value")) {
					faults = append(faults, fmt.Sprintf("%q / query %d: a chart reads time, ..., value; got %v", p.Title, i, cols))
				}
			}
		}
	}
	return faults, ran
}

func has(cols []string, want string) bool {
	for _, c := range cols {
		if c == want {
			return true
		}
	}
	return false
}
