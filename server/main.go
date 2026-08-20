// Ammit weighs a running pipeline against its limits, from outside it.
//
// A four-hour budget enforced by a timer inside the pipeline fired at ten and a
// half hours, because the pipeline never gave the timer a turn: $345 to learn
// that the thing being limited must not be in charge of the limit. A session cap
// in the same loop cut sessions in half while they waited for tests. A single
// turn hung for two hours with no error and no retry, twice in one run.
//
// So: one binary, in its own container, holding no state the pipeline can
// corrupt. It keeps every event it is told about, weighs them on a tick against
// a config file, and acts by running a command — which is why it never needs to
// know whether it watches a container, a pod or a process.
//
// The scales, the record and the eating. Nothing else.
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS events (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    at      REAL NOT NULL,
    kind    TEXT NOT NULL,
    run     TEXT,
    phase   TEXT,
    session TEXT,
    agent   TEXT,
    branch  TEXT,
    payload TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS events_run ON events (run, at);
CREATE INDEX IF NOT EXISTS events_kind ON events (kind, at);

CREATE TABLE IF NOT EXISTS runs (
    run      TEXT PRIMARY KEY,
    name     TEXT,
    started  REAL,
    finished REAL,
    verdict  TEXT,
    summary  TEXT,
    usd      REAL DEFAULT 0,
    turns    INTEGER DEFAULT 0
);

CREATE TABLE IF NOT EXISTS judgements (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    at        REAL NOT NULL,
    run       TEXT,
    scope     TEXT NOT NULL,
    subject   TEXT,
    rule      TEXT NOT NULL,
    threshold REAL,
    observed  REAL,
    action    TEXT NOT NULL,
    outcome   TEXT
);
CREATE INDEX IF NOT EXISTS judgements_at ON judgements (at);

CREATE TABLE IF NOT EXISTS queue (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    name      TEXT NOT NULL,
    payload   TEXT,
    requested REAL NOT NULL,
    started   REAL,
    finished  REAL,
    run       TEXT,
    state     TEXT NOT NULL DEFAULT 'waiting'
);`

var (
	db     *sql.DB
	mu     sync.Mutex
	dryRun = os.Getenv("AMMIT_DRY_RUN") == "1"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Config is two levels of key/value, read fresh on every tick.
//
// Fresh rather than cached because a limit gets changed while a run is going,
// which is exactly when somebody wants to change one. Two levels because that is
// all the file needs, and a yaml dependency in the one service whose value is
// having fewer moving parts than what it watches would be a poor trade.
type Config map[string]map[string]string

func loadConfig(path string) Config {
	conf := Config{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return conf
	}
	section := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ":"))
			conf[section] = map[string]string{}
			continue
		}
		if section == "" {
			continue
		}
		key, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found {
			continue
		}
		conf[section][strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return conf
}

func (c Config) num(section, key string) (float64, bool) {
	v, ok := c[section][key]
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f == 0 {
		return 0, false // zero means no limit, and saying zero is a decision
	}
	return f, true
}

func (c Config) str(section, key, fallback string) string {
	if v, ok := c[section][key]; ok && v != "" {
		return v
	}
	return fallback
}

type event map[string]any

func (e event) s(key string) string {
	if v, ok := e[key].(string); ok {
		return v
	}
	return ""
}

func (e event) f(key string) float64 {
	if v, ok := e[key].(float64); ok {
		return v
	}
	return 0
}

func store(e event) {
	mu.Lock()
	defer mu.Unlock()
	at := e.f("at")
	if at == 0 {
		at = float64(time.Now().UnixNano()) / 1e9
	}
	payload, _ := json.Marshal(e)
	if _, err := db.Exec(
		`INSERT INTO events (at, kind, run, phase, session, agent, branch, payload)
		 VALUES (?,?,?,?,?,?,?,?)`,
		at, e.s("kind"), e.s("run"), e.s("phase"), e.s("session"), e.s("agent"),
		e.s("branch"), string(payload)); err != nil {
		log.Printf("ammit: could not keep an event: %v", err)
		return
	}
	switch e.s("kind") {
	case "run_start":
		db.Exec(`INSERT OR REPLACE INTO runs (run, name, started) VALUES (?,?,?)`,
			e.s("run"), e.s("name"), at)
	case "run_end":
		db.Exec(`UPDATE runs SET finished=?, verdict=?, summary=? WHERE run=?`,
			at, e.s("verdict"), e.s("summary"), e.s("run"))
		db.Exec(`UPDATE queue SET state='done', finished=? WHERE run=?`, at, e.s("run"))
	case "spend":
		db.Exec(`UPDATE runs SET usd = coalesce(usd,0) + ? WHERE run=?`,
			e.f("usd"), e.s("run"))
	case "turn":
		db.Exec(`UPDATE runs SET turns = coalesce(turns,0) + 1 WHERE run=?`, e.s("run"))
	}
}

// judge writes down what was weighed and what came of it.
//
// A limit crossed silently is not a limit, and a kill nobody recorded is
// indistinguishable from a crash.
func judge(scope, run, subject, rule string, threshold, observed float64,
	action, outcome string) {
	mu.Lock()
	db.Exec(`INSERT INTO judgements (at, run, scope, subject, rule, threshold,
	         observed, action, outcome) VALUES (?,?,?,?,?,?,?,?,?)`,
		float64(time.Now().UnixNano())/1e9, run, scope, subject, rule, threshold,
		observed, action, outcome)
	mu.Unlock()
	log.Printf("ammit: %s — %.0f against %.0f on %s %s -> %s %s",
		rule, observed, threshold, orDash(run), subject, action, outcome)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// act runs the command the config names, with this run's details substituted.
//
// Commands rather than code: the watchdog does not need to know what kind of
// thing it is watching, only what to run when a number crosses a line.
func act(name string, conf Config, ctx map[string]string) string {
	tmpl := conf.str("commands", name, "")
	if tmpl == "" {
		return "no command named " + name
	}
	for key, value := range ctx {
		tmpl = strings.ReplaceAll(tmpl, "{"+key+"}", value)
	}
	if dryRun {
		return "[dry run] " + tmpl
	}
	out, err := exec.Command("sh", "-c", tmpl).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("failed: %v", err)
	}
	said := strings.TrimSpace(string(out))
	if len(said) > 200 {
		said = said[:200]
	}
	if said == "" {
		return "done"
	}
	return said
}

func finish(run, verdict, summary string) {
	mu.Lock()
	defer mu.Unlock()
	now := float64(time.Now().UnixNano()) / 1e9
	db.Exec(`UPDATE runs SET finished=?, verdict=?, summary=? WHERE run=?`,
		now, verdict, summary, run)
	db.Exec(`UPDATE queue SET state='done', finished=? WHERE run=?`, now, run)
}

type openRun struct {
	run, name    string
	started, usd float64
	turns        int
}

func openRuns() []openRun {
	mu.Lock()
	defer mu.Unlock()
	rows, err := db.Query(`SELECT run, coalesce(name,''), started, coalesce(usd,0),
	                       coalesce(turns,0) FROM runs
	                       WHERE finished IS NULL AND started > ?`,
		float64(time.Now().Add(-72*time.Hour).UnixNano())/1e9)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []openRun
	for rows.Next() {
		var r openRun
		if err := rows.Scan(&r.run, &r.name, &r.started, &r.usd, &r.turns); err == nil {
			out = append(out, r)
		}
	}
	return out
}

// quietFor is how long a run has said nothing.
//
// The client reports a turn per exchange, so silence inside a live run is a turn
// that has not come back — which is the failure this whole service was written
// for, and the one nothing inside the pipeline could see.
func quietFor(run string) (float64, string, string) {
	mu.Lock()
	defer mu.Unlock()
	var at float64
	var agent, phase string
	err := db.QueryRow(`SELECT at, coalesce(agent,''), coalesce(phase,'') FROM events
	                    WHERE run=? ORDER BY id DESC LIMIT 1`, run).Scan(&at, &agent, &phase)
	if err != nil {
		return 0, "", ""
	}
	return float64(time.Now().UnixNano())/1e9 - at, agent, phase
}

func openSpans(run, startKind, endKind, column string) map[string]float64 {
	mu.Lock()
	defer mu.Unlock()
	rows, err := db.Query(fmt.Sprintf(
		`SELECT coalesce(%s,''), kind, at FROM events WHERE run=? AND kind IN (?,?)
		 ORDER BY id`, column), run, startKind, endKind)
	if err != nil {
		return nil
	}
	defer rows.Close()
	live := map[string]float64{}
	for rows.Next() {
		var key, kind string
		var at float64
		if err := rows.Scan(&key, &kind, &at); err != nil {
			continue
		}
		if kind == startKind {
			live[key] = at
		} else {
			delete(live, key)
		}
	}
	now := float64(time.Now().UnixNano()) / 1e9
	for key, t0 := range live {
		live[key] = now - t0
	}
	return live
}

func weigh(conf Config) {
	for _, r := range openRuns() {
		age := float64(time.Now().UnixNano())/1e9 - r.started
		ctx := map[string]string{"run": r.run, "name": r.name}
		for k, v := range conf["context"] {
			ctx[k] = v
		}

		if limit, ok := conf.num("timeouts", "run"); ok && age > limit {
			action := conf.str("actions", "on_run_timeout", "stop_run")
			judge("run", r.run, r.name, "timeouts.run", limit, age, action,
				act(action, conf, ctx))
			finish(r.run, "BLOCKED", fmt.Sprintf("ammit: over timeouts.run (%.0fs)", limit))
			continue
		}
		if limit, ok := conf.num("limits", "usd_per_run"); ok && r.usd > limit {
			action := conf.str("actions", "on_usd", "stop_run")
			judge("run", r.run, r.name, "limits.usd_per_run", limit, r.usd, action,
				act(action, conf, ctx))
			finish(r.run, "BLOCKED", "ammit: over limits.usd_per_run")
			continue
		}
		if limit, ok := conf.num("limits", "turns_per_run"); ok && float64(r.turns) > limit {
			judge("run", r.run, r.name, "limits.turns_per_run", limit, float64(r.turns),
				"warn", "")
		}

		if limit, ok := conf.num("timeouts", "turn"); ok {
			if quiet, agent, phase := quietFor(r.run); quiet > limit {
				action := conf.str("actions", "on_turn_timeout", "warn")
				ctx["agent"], ctx["phase"] = agent, phase
				judge("turn", r.run, strings.TrimSpace(agent+" "+phase), "timeouts.turn",
					limit, quiet, action, act(action, conf, ctx))
			}
		}
		if limit, ok := conf.num("timeouts", "phase"); ok {
			for phase, age := range openSpans(r.run, "phase_start", "phase_end", "phase") {
				if age > limit {
					action := conf.str("actions", "on_phase_timeout", "warn")
					ctx["phase"] = phase
					judge("phase", r.run, phase, "timeouts.phase", limit, age, action,
						act(action, conf, ctx))
				}
			}
		}
		if limit, ok := conf.num("timeouts", "session"); ok {
			for session, age := range openSpans(r.run, "session_start", "session_end", "session") {
				if age > limit {
					action := conf.str("actions", "on_session_timeout", "warn")
					ctx["session"] = session
					judge("session", r.run, session, "timeouts.session", limit, age,
						action, act(action, conf, ctx))
				}
			}
		}
	}
}

// pumpQueue starts the next item when a slot frees, in order.
//
// Parallelism becomes a number in a file rather than a property of whoever
// pressed the button first.
func pumpQueue(conf Config) {
	slots := 1.0
	if v, ok := conf.num("queue", "parallel"); ok {
		slots = v
	}
	mu.Lock()
	var active int
	db.QueryRow(`SELECT count(*) FROM runs WHERE finished IS NULL AND started > ?`,
		float64(time.Now().Add(-24*time.Hour).UnixNano())/1e9).Scan(&active)
	var id int64
	var name, payload string
	err := db.QueryRow(`SELECT id, name, coalesce(payload,'') FROM queue
	                    WHERE state='waiting' ORDER BY requested LIMIT 1`).
		Scan(&id, &name, &payload)
	mu.Unlock()
	if err != nil || float64(active) >= slots {
		return
	}
	ctx := map[string]string{"name": name, "payload": payload}
	for k, v := range conf["context"] {
		ctx[k] = v
	}
	outcome := act(conf.str("actions", "on_start", "start_run"), conf, ctx)
	mu.Lock()
	db.Exec(`UPDATE queue SET state='running', started=? WHERE id=?`,
		float64(time.Now().UnixNano())/1e9, id)
	mu.Unlock()
	judge("queue", "", name, "queue.parallel", slots, float64(active+1), "started", outcome)
}

func rows2json(w http.ResponseWriter, query string) {
	mu.Lock()
	rows, err := db.Query(query)
	mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []map[string]any
	for rows.Next() {
		cells := make([]any, len(cols))
		holders := make([]any, len(cols))
		for i := range cells {
			holders[i] = &cells[i]
		}
		if err := rows.Scan(holders...); err != nil {
			continue
		}
		row := map[string]any{}
		for i, c := range cols {
			row[c] = cells[i]
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}

func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(payload)
}

var validName = regexp.MustCompile(`^[\w.:/-]{1,120}$`)

func main() {
	dbPath := env("AMMIT_DB", "/data/ammit.db")
	confPath := env("AMMIT_CONFIG", "/config/limits.yml")
	port := env("AMMIT_PORT", "8099")
	tick, _ := strconv.Atoi(env("AMMIT_TICK", "20"))

	if err := os.MkdirAll(strings.TrimSuffix(dbPath, "/"+lastSegment(dbPath)), 0o755); err != nil {
		log.Printf("ammit: could not make the data directory: %v", err)
	}
	var err error
	db, err = sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(30000)")
	if err != nil {
		log.Fatalf("ammit: no database: %v", err)
	}
	if _, err := db.Exec(schema); err != nil {
		log.Fatalf("ammit: could not make the tables: %v", err)
	}

	go func() {
		for {
			conf := loadConfig(confPath)
			if len(conf) > 0 {
				weigh(conf)
				pumpQueue(conf)
			}
			time.Sleep(time.Duration(tick) * time.Second)
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /events", func(w http.ResponseWriter, r *http.Request) {
		var e event
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not json"})
			return
		}
		store(e)
		writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /queue", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name    string `json:"name"`
			Payload any    `json:"payload"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || !validName.MatchString(in.Name) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
			return
		}
		payload, _ := json.Marshal(in.Payload)
		mu.Lock()
		db.Exec(`INSERT INTO queue (name, payload, requested) VALUES (?,?,?)`,
			in.Name, string(payload), float64(time.Now().UnixNano())/1e9)
		mu.Unlock()
		writeJSON(w, http.StatusCreated, map[string]string{"queued": in.Name})
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "db": dbPath})
	})
	mux.HandleFunc("GET /runs", func(w http.ResponseWriter, r *http.Request) {
		rows2json(w, `SELECT * FROM runs ORDER BY started DESC LIMIT 50`)
	})
	mux.HandleFunc("GET /judgements", func(w http.ResponseWriter, r *http.Request) {
		rows2json(w, `SELECT * FROM judgements ORDER BY id DESC LIMIT 200`)
	})
	mux.HandleFunc("GET /queue", func(w http.ResponseWriter, r *http.Request) {
		rows2json(w, `SELECT * FROM queue ORDER BY id DESC LIMIT 100`)
	})
	mux.HandleFunc("GET /limits", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, loadConfig(confPath))
	})

	log.Printf("ammit: listening on :%s, limits from %s, db %s%s", port, confPath, dbPath,
		map[bool]string{true: " (dry run)", false: ""}[dryRun])
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

func lastSegment(path string) string {
	parts := strings.Split(path, "/")
	return parts[len(parts)-1]
}
