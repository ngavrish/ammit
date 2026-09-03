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
-- Spans are looked up by what is still open in one run, which is a question
-- about (run, kind, session) and nothing else.
CREATE INDEX IF NOT EXISTS events_span ON events (run, kind, session);
CREATE INDEX IF NOT EXISTS events_phase ON events (run, kind, phase);

-- What a token costs, per model, USD per million - the SDK's own catalog,
-- with both cache-write rates and the US surcharge, so a turn is priced as
-- the bill prices it. Seeded from the embedded prices.json on every start.
-- A session that dies before its bill arrives still paid for every turn it
-- took, and the ledger's spend row never comes for it.
CREATE TABLE IF NOT EXISTS prices (
    model          TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    tier           TEXT NOT NULL,
    input          REAL NOT NULL,
    output         REAL NOT NULL,
    cache_read     REAL NOT NULL,
    cache_write    REAL NOT NULL,
    cache_write_1h REAL NOT NULL,
    us_surcharge   REAL NOT NULL
);

-- Three more things lifted out of the event stream into columns, the way
-- gates and calls already are: a chart reads a column, not json_extract over
-- every event there has been, and the same expression is not repeated in
-- seventy queries. Keyed by the event they came from, so lifting is
-- idempotent and history can be lifted once on start.
CREATE TABLE IF NOT EXISTS turns (
    event_id       INTEGER PRIMARY KEY,
    at             REAL NOT NULL,
    run            TEXT,
    agent          TEXT,
    phase          TEXT,
    branch         TEXT,
    request        TEXT,            -- the wait this turn answered (sid#seq)
    model          TEXT,
    context        INTEGER,         -- everything the turn was sent
    tokens_in      INTEGER,         -- NULL: the runner did not split the context yet
    tokens_out     INTEGER,         -- exactly what the SDK reported (often the stream's placeholder)
    out_est        INTEGER,         -- the runner's measure of the message, four characters a token
    cache_read     INTEGER,
    cache_write    INTEGER,
    cache_write_1h INTEGER,         -- the share of cache_write kept an hour
    geo            TEXT             -- where it was inferred; "us" costs 1.1
);
CREATE INDEX IF NOT EXISTS turns_run ON turns (run, at);
CREATE INDEX IF NOT EXISTS turns_at ON turns (at);

CREATE TABLE IF NOT EXISTS suites (
    event_id INTEGER PRIMARY KEY,
    at       REAL NOT NULL,
    run      TEXT,
    branch   TEXT NOT NULL DEFAULT '',
    verdict  TEXT NOT NULL,          -- green | red
    total    INTEGER,
    passed   INTEGER,
    failed   INTEGER,
    reason   TEXT NOT NULL DEFAULT ''-- why red, in the suite's own words
);
CREATE INDEX IF NOT EXISTS suites_run ON suites (run, branch, at);

CREATE TABLE IF NOT EXISTS heal_laps (
    event_id INTEGER PRIMARY KEY,
    at       REAL NOT NULL,
    run      TEXT,
    branch   TEXT NOT NULL DEFAULT '',
    phase    TEXT,
    lap      INTEGER NOT NULL,
    cap      INTEGER,                -- NULL: lifted from history, the cap was not recorded
    decision TEXT NOT NULL           -- again | converged | gave_up | unknown
);
CREATE INDEX IF NOT EXISTS heal_laps_run ON heal_laps (run, branch, at);

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

-- What a check said, and how many rounds it took to stop saying it.
--
-- A pipeline that reviews its own work has two questions nobody could answer:
-- how often each check refuses, and how long the repair takes. Both are the
-- difference between a gate that is earning its cost and a gate that is a
-- tollbooth. Reported by the pipeline rather than read out of its logs, because
-- what counts as a finding is the pipeline's business and not this service's.
CREATE TABLE IF NOT EXISTS gates (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    at       REAL NOT NULL,
    run      TEXT,
    phase    TEXT NOT NULL,
    branch   TEXT,
    verdict  TEXT NOT NULL,      -- red | green | blocked
    findings INTEGER DEFAULT 0,
    round    INTEGER DEFAULT 1,  -- how many times this check has run here
    seconds  REAL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS gates_run ON gates (run, phase);

-- Every call an agent made: a shell command, a file read, a search, an MCP
-- tool, one of our own command-line programs. One row each, with what makes two
-- of them the same thing written down beside them.
--
-- The transcript already held these, as display strings inside a log line, and a
-- question as ordinary as "what did this session do twice" meant parsing prose.
-- Measured by hand that way: one branch read a single step module twenty times
-- and ran the same grep for step phrases eleven times. Nobody knew until
-- somebody counted, and counting is what a database is for.
CREATE TABLE IF NOT EXISTS calls (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    at        REAL NOT NULL,
    run       TEXT,
    phase     TEXT,
    branch    TEXT,
    agent     TEXT,
    session   TEXT,
    tool      TEXT NOT NULL,       -- Bash | Read | Grep | mcp__map | …
    kind      TEXT,                -- search | read | write | test | map | cli | other
    target    TEXT,                -- what it acted on: a path, a pattern, a program
    signature TEXT NOT NULL,       -- normalised: what makes two calls the same call
    repeat    INTEGER DEFAULT 1,   -- how many times this signature has run in this run
    on_target INTEGER DEFAULT 1,   -- and how many times anything has touched this target
    seconds   REAL DEFAULT 0,
    ok        INTEGER DEFAULT 1,
    -- What the caller said it was for. Arbitrary python has to name a reason
    -- from a fixed list, and a reason nobody records is a reason nobody can
    -- check afterwards: the guardrail refuses at the moment of the call, and
    -- this is what lets a gate ask, later, whether the reasons a phase gave
    -- were the reasons it had.
    why       TEXT DEFAULT ''
);

-- The phase sequence a run actually executed.
--
-- Two runs of one ticket were compared on turns and money and the conclusion
-- was wrong, because their flows differed and nothing recorded it: one had the
-- designer merged into the implementer, the other did not, and finding that out
-- meant reading commit diffs of a file the orchestrator rewrites itself. A
-- flow is a property of a run. It belongs here, beside the turns and the
-- dollars, written when the run opens.
CREATE TABLE IF NOT EXISTS flows (
    run    TEXT PRIMARY KEY,
    at     REAL,
    mode   TEXT,
    phases TEXT
);
CREATE INDEX IF NOT EXISTS calls_run ON calls (run, signature);
CREATE INDEX IF NOT EXISTS calls_target ON calls (run, target);

CREATE TABLE IF NOT EXISTS documents (
    id    INTEGER PRIMARY KEY AUTOINCREMENT,
    at    REAL NOT NULL,
    run   TEXT,
    kind  TEXT NOT NULL,
    phase TEXT,
    bytes INTEGER,
    path  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS documents_run ON documents (run, kind);

-- How a run is found: by what it is called, by when it started, or by the id
-- that is neither. A person asks for APF-1934 and means the one from Thursday
-- afternoon; a query asks for the uuid and means exactly one row.
--
-- The pair is unique. Nothing enforced it before, and two rows claiming the same
-- ticket at the same instant would both have been answered to — quietly, with
-- whichever the query happened to reach first.
CREATE UNIQUE INDEX IF NOT EXISTS runs_named ON runs (name, started);
CREATE INDEX IF NOT EXISTS runs_started ON runs (started);
CREATE INDEX IF NOT EXISTS runs_name ON runs (name, started DESC);

-- Everything a run produced, found by the run that produced it. events already
-- had one; the others were full-table scans that nobody had noticed yet because
-- the tables were small.
CREATE INDEX IF NOT EXISTS judgements_run ON judgements (run, at);
CREATE INDEX IF NOT EXISTS gates_run_at ON gates (run, at);
CREATE INDEX IF NOT EXISTS calls_run_at ON calls (run, at);

CREATE TABLE IF NOT EXISTS limits (
    id    INTEGER PRIMARY KEY AUTOINCREMENT,
    at    REAL NOT NULL,
    name  TEXT NOT NULL,
    value REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS limits_name ON limits (name, at);

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
	docsDir string
	db      *sql.DB
	mu      sync.Mutex
	dryRun  = os.Getenv("AMMIT_DRY_RUN") == "1"
)

// {word} and nothing else: a substituted payload carries JSON braces of its
// own — {"key":"APF-1934"} — and those are not holes.
var placeholder = regexp.MustCompile(`\{[a-zA-Z_][a-zA-Z0-9_]*\}`)

var reanimations = map[string]int{}

// The timeout names this service owns. A client cannot claim one of these as a
// kind, because "my module took longer than timeouts.run" is not a sentence
// anyone wants to debug.
var reserved = map[string]bool{
	"run": true, "phase": true, "session": true, "request": true,
	"request_tool": true, "turn": true,
}

func rows2json(w http.ResponseWriter, query string, args ...any) {
	mu.Lock()
	rows, err := db.Query(query, args...)
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

// openDB opens or makes the database at path and brings it up to date: the
// schema, the columns added since, the price sheet, the history lifted into
// its tables. main and the tests share it: a test that runs every panel's
// SQL is only worth having if it runs against the schema the service runs.
func openDB(dbPath string) error {
	if err := os.MkdirAll(strings.TrimSuffix(dbPath, "/"+lastSegment(dbPath)), 0o755); err != nil {
		log.Printf("ammit: could not make the data directory: %v", err)
	}
	var err error
	db, err = sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(30000)")
	if err != nil {
		return err
	}
	dropOldPrices()
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("could not make the tables: %w", err)
	}
	// Columns added to a table that already exists. CREATE TABLE IF NOT EXISTS
	// does nothing to a database that has the table, so a new column arrives
	// only this way. Each is tried and its error ignored: "duplicate column
	// name" is the expected outcome on every start but the first, and a
	// migration that refuses to be run twice has to be tracked, which is a
	// second thing to get wrong.
	for _, add := range []string{
		`ALTER TABLE calls ADD COLUMN why TEXT DEFAULT ''`,
		// The wait the call happened in (sid#seq), so a call and the turn that
		// asked for it are one trace.
		`ALTER TABLE calls ADD COLUMN request TEXT DEFAULT ''`,
	} {
		if _, err := db.Exec(add); err != nil &&
			!strings.Contains(err.Error(), "duplicate column") {
			log.Printf("ammit: %s (%v)", add, err)
		}
	}
	seedPrices()
	liftHistory()
	return nil
}

func main() {
	dbPath := env("AMMIT_DB", "/data/ammit.db")
	docsDir = env("AMMIT_DOCS", strings.TrimSuffix(dbPath, "/"+lastSegment(dbPath))+"/documents")
	confPath := env("AMMIT_CONFIG", "/config/limits.yml")
	port := env("AMMIT_PORT", "8099")
	tick, _ := strconv.Atoi(env("AMMIT_TICK", "20"))

	if err := openDB(dbPath); err != nil {
		log.Fatalf("ammit: no database: %v", err)
	}

	// Readings on their own thread. Judging must never wait for a machine
	// reading: one is a call out to a daemon that answers when it feels like it,
	// the other is the entire point of this service. They shared a loop for one
	// night and the daemon won — eighteen minutes at a stretch with nothing
	// weighed at all, while a run was going.
	go func() {
		for {
			if conf := loadConfig(confPath); len(conf) > 0 {
				keepSamples(conf)
			}
			time.Sleep(15 * time.Second)
		}
	}()

	go func() {
		for {
			conf := loadConfig(confPath)
			if len(conf) > 0 {
				recordLimits(conf)
				sweepAbandoned(conf)
				sweepQueue(conf)
				weigh(conf)
				pumpQueue(conf)
				// Every tick: deciding whether there is anything to archive is one
				// indexed count, and an hourly guard only made it harder to tell
				// whether archiving works at all.
				archive(conf, dbPath)
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
	mux.HandleFunc("POST /documents", func(w http.ResponseWriter, r *http.Request) {
		// A phase's artefact — a framework map, the requirements, a report. The
		// body goes to a file and the row keeps the path: a run's map is over a
		// megabyte, and a database that swallows those is a database nobody
		// wants to keep for a year.
		var in struct {
			Run   string `json:"run"`
			Kind  string `json:"kind"`
			Phase string `json:"phase"`
			Body  string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Kind == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "kind and body are required"})
			return
		}
		dir := docsDir + "/" + safeName(in.Run)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		path := fmt.Sprintf("%s/%s-%d", dir, safeName(in.Kind), time.Now().Unix())
		if err := os.WriteFile(path, []byte(in.Body), 0o644); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		mu.Lock()
		db.Exec(`INSERT INTO documents (at, run, kind, phase, bytes, path)
		         VALUES (?,?,?,?,?,?)`,
			float64(time.Now().UnixNano())/1e9, in.Run, in.Kind, in.Phase,
			len(in.Body), path)
		mu.Unlock()
		writeJSON(w, http.StatusCreated, map[string]any{"path": path, "bytes": len(in.Body)})
	})
	mux.HandleFunc("GET /documents", func(w http.ResponseWriter, r *http.Request) {
		if run := r.URL.Query().Get("run"); run != "" && r.URL.Query().Get("kind") != "" {
			// The newest of that kind, as the file itself.
			var path string
			mu.Lock()
			err := db.QueryRow(`SELECT path FROM documents WHERE run=? AND kind=?
			                    ORDER BY id DESC LIMIT 1`, run,
				r.URL.Query().Get("kind")).Scan(&path)
			mu.Unlock()
			if err != nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such document"})
				return
			}
			http.ServeFile(w, r, path)
			return
		}
		rows2json(w, `SELECT id, at, run, kind, coalesce(phase,'') phase, bytes, path
		              FROM documents ORDER BY id DESC LIMIT 200`)
	})
	// One free-form question against the whole record, for the chat, the CLI
	// and anybody with curl. SELECT only - the same seatbelt the saved charts
	// wear - because this service's tables are the pipeline's memory, and a
	// memory that a query can edit is not a record.
	mux.HandleFunc("GET /query", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("sql")
		if q == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sql parameter required"})
			return
		}
		if !readsOnly(q) {
			writeJSON(w, http.StatusBadRequest,
				map[string]string{"error": "reads only: the query must start with SELECT or WITH"})
			return
		}
		writeJSON(w, http.StatusOK, rows(q))
	})

	// The same levers ammit pulls, offered to a hand. The command must be one
	// limits.yml already names - stop_run, retry_phase, start_run - so a chat
	// or a CLI can never do anything the config did not spell out, and every
	// pull is a judgement row with scope "hand": a stop ordered in
	// conversation reads in the record exactly like one a limit ordered.
	mux.HandleFunc("POST /act", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Command string `json:"command"`
			Run     string `json:"run"`
			Name    string `json:"name"`
			Phase   string `json:"phase"`
			Agent   string `json:"agent"`
			Branch  string `json:"branch"`
			Session string `json:"session"`
			Payload string `json:"payload"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Command == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "command is required"})
			return
		}
		conf := loadConfig(confPath)
		if conf.str("commands", in.Command, "") == "" {
			writeJSON(w, http.StatusBadRequest,
				map[string]string{"error": "no command named " + in.Command + " in limits.yml"})
			return
		}
		// Only what the caller actually said: an absent field must stay a hole
		// so act()'s never-filled guard refuses the command, rather than a
		// silent "" that turns stop_run into touching /runs//.cancel.
		ctx := map[string]string{}
		for key, value := range map[string]string{
			"run": in.Run, "name": in.Name, "phase": in.Phase,
			"agent": in.Agent, "branch": in.Branch, "session": in.Session,
			"payload": in.Payload,
		} {
			if value != "" {
				ctx[key] = value
			}
		}
		for key, value := range conf["context"] {
			if ctx[key] == "" {
				ctx[key] = value
			}
		}
		outcome := act(in.Command, conf, ctx)
		judge("hand", in.Run, in.Name, "hand."+in.Command, 0, 0, in.Command, outcome)
		writeJSON(w, http.StatusOK, map[string]string{"outcome": outcome})
	})

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "db": dbPath})
	})
	mux.HandleFunc("GET /runs", func(w http.ResponseWriter, r *http.Request) {
		rows2json(w, `SELECT * FROM runs ORDER BY started DESC LIMIT 50`)
	})
	mux.HandleFunc("GET /gates", func(w http.ResponseWriter, r *http.Request) {
		rows2json(w, `SELECT * FROM gates ORDER BY id DESC LIMIT 500`)
	})

	// Every call, newest first. ?run= narrows it; ?repeats=1 keeps only the ones
	// that had already run — which is the question this table was added for.
	// What a run executed, asked for by run or listed newest first. The point of
	// keeping it is comparing two runs and knowing whether they ran the same
	// thing; that question is a query now rather than a git archaeology.
	mux.HandleFunc("GET /flows", func(w http.ResponseWriter, r *http.Request) {
		where, args := "1=1", []any{}
		if run := r.URL.Query().Get("run"); run != "" {
			where, args = "run=?", []any{run}
		}
		rows2json(w, `SELECT * FROM flows WHERE `+where+` ORDER BY at DESC LIMIT 50`,
			args...)
	})

	mux.HandleFunc("GET /calls", func(w http.ResponseWriter, r *http.Request) {
		where, args := "1=1", []any{}
		if run := r.URL.Query().Get("run"); run != "" {
			where, args = where+" AND run=?", append(args, run)
		}
		if r.URL.Query().Get("repeats") != "" {
			where += " AND repeat > 1"
		}
		rows2json(w, `SELECT * FROM calls WHERE `+where+
			` ORDER BY id DESC LIMIT 2000`, args...)
	})

	// The same calls, added up: what ran more than once and how often. One row
	// per distinct call rather than per occurrence, because "this session made
	// four hundred and fifty-five calls" is not a finding and "it ran this one
	// eleven times" is.
	mux.HandleFunc("GET /calls/repeated", func(w http.ResponseWriter, r *http.Request) {
		where, args := "1=1", []any{}
		if run := r.URL.Query().Get("run"); run != "" {
			where, args = where+" AND run=?", append(args, run)
		}
		rows2json(w, `SELECT max(run) AS run, agent, branch, kind, tool, target,
		              signature, count(*) AS times, round(sum(seconds),1) AS seconds
		              FROM calls WHERE `+where+`
		              GROUP BY agent, branch, signature
		              HAVING times > 1 ORDER BY times DESC LIMIT 500`, args...)
	})

	// And by what was touched rather than how: the same file read with Read,
	// then with cat, then with three sed windows is five calls and one target.
	mux.HandleFunc("GET /calls/targets", func(w http.ResponseWriter, r *http.Request) {
		where, args := "1=1", []any{}
		if run := r.URL.Query().Get("run"); run != "" {
			where, args = where+" AND run=?", append(args, run)
		}
		// Every kind that touched it, not whichever row the group happened to
		// yield: a file that was searched six times and written once came back
		// labelled "write", which is the opposite of what it says.
		rows2json(w, `SELECT max(run) AS run, agent, branch, target,
		              group_concat(DISTINCT kind) AS kinds,
		              count(*) AS times, count(DISTINCT signature) AS ways
		              FROM calls WHERE `+where+` AND ifnull(target,'') <> ''
		              GROUP BY agent, branch, target
		              HAVING times > 1 ORDER BY times DESC LIMIT 500`, args...)
	})

	// What waiting on the model actually bought. A request_end carries how long
	// it took and how much came back, and until now both went into the events
	// table and stayed there: the client had been sending the size for a week
	// and nothing could read it.
	//
	// The pair is the point. Sixty-seven requests of one run's ten thousand took
	// a quarter of all the time spent waiting, every one of them ok and without
	// an error, and there was no way to tell nine minutes of long answer from
	// nine minutes of stall. Now there is — the row says both.
	//
	// ?slow= puts the longest first instead of the newest, which is the order
	// you want the moment you are asking this question at all.
	//
	// Spans ammit closed itself are not requests and are left out. A run whose
	// worker was killed leaves its spans open, ammit shuts them at the run's
	// end and records the age as the duration, and those ages are hours: the
	// first answer this endpoint ever gave was five "requests" averaging
	// twenty-five hours, which is a killed run's bookkeeping sorted to the top
	// of a list about slowness. ?closed= brings them back for anyone asking
	// about the closing rather than about the waiting.
	mux.HandleFunc("GET /requests", func(w http.ResponseWriter, r *http.Request) {
		where, args := "kind='request_end'"+notClosed(r), []any{}
		if run := r.URL.Query().Get("run"); run != "" {
			where, args = where+" AND run=?", append(args, run)
		}
		if ph := r.URL.Query().Get("phase"); ph != "" {
			where, args = where+" AND phase=?", append(args, ph)
		}
		order := "at DESC"
		if r.URL.Query().Get("slow") != "" {
			order = "seconds DESC"
		}
		rows2json(w, `SELECT at, run, agent, branch, phase, session,
		              json_extract(payload,'$.seconds') AS seconds,
		              json_extract(payload,'$.out')     AS out,
		              json_extract(payload,'$.ok')      AS ok,
		              json_extract(payload,'$.error')   AS error
		              FROM events WHERE `+where+`
		              ORDER BY `+order+` LIMIT 2000`, args...)
	})

	// The same requests added up per agent, which is where the question starts:
	// who waits, for how long in total, and what they get per second of it. A
	// phase whose tokens-per-second is an order off its neighbours' is not slow
	// because the answers are long.
	//
	// Tokens, not bytes: the client sent `out` as characters of visible text
	// for some messages and the SDK's token count for others, so a sum over
	// this column added two units together. It is tokens throughout now, and
	// a ResultMessage — which carries the session's cumulative output, not one
	// request's — no longer lands here at all.
	mux.HandleFunc("GET /requests/by-agent", func(w http.ResponseWriter, r *http.Request) {
		where, args := "kind='request_end'"+notClosed(r), []any{}
		if run := r.URL.Query().Get("run"); run != "" {
			where, args = where+" AND run=?", append(args, run)
		}
		rows2json(w, `SELECT agent, phase, count(*) AS times,
		              round(sum(json_extract(payload,'$.seconds')),1) AS seconds,
		              round(avg(json_extract(payload,'$.seconds')),1) AS mean,
		              sum(json_extract(payload,'$.out')) AS out,
		              round(sum(json_extract(payload,'$.out')) /
		                    max(sum(json_extract(payload,'$.seconds')),1), 1)
		                AS out_per_second
		              FROM events WHERE `+where+`
		              GROUP BY agent, phase
		              ORDER BY seconds DESC LIMIT 500`, args...)
	})

	// Is anything running right now — asked by whoever is about to disturb it.
	//
	// Not "is there a row without a finish": a run whose worker was killed leaves
	// one of those behind for as long as it takes something to notice, and on 21
	// August that was fourteen hours. A run that is alive says so continuously —
	// a turn, a call, a phase — so this asks when it last said anything.
	//
	// ?quiet= is how long a run may be silent and still count as running.
	// Default two minutes: longer than any gap a working run leaves, shorter than
	// any outage worth deploying over.
	mux.HandleFunc("GET /inflight", func(w http.ResponseWriter, r *http.Request) {
		quiet := 120.0
		if v := r.URL.Query().Get("quiet"); v != "" {
			if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
				quiet = n
			}
		}
		mu.Lock()
		rows, err := db.Query(`SELECT r.run, coalesce(r.name,''), r.started,
		                       coalesce(max(e.at), r.started) AS last
		                       FROM runs r LEFT JOIN events e ON e.run = r.run
		                       WHERE r.finished IS NULL GROUP BY r.run`)
		type live struct {
			Run    string  `json:"run"`
			Name   string  `json:"name"`
			Silent float64 `json:"silent_seconds"`
		}
		var out []live
		now := float64(time.Now().UnixNano()) / 1e9
		if err == nil {
			for rows.Next() {
				var l live
				var started, last float64
				if rows.Scan(&l.Run, &l.Name, &started, &last) == nil {
					l.Silent = now - last
					if l.Silent <= quiet {
						out = append(out, l)
					}
				}
			}
			rows.Close()
		}
		mu.Unlock()
		if out == nil {
			out = []live{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"quiet_allowed": quiet, "running": out})
	})

	// What the services themselves said. Their own logs lived in their
	// containers, which are recreated by every deploy — so the record of a
	// failure outlived the failure by minutes.
	mux.HandleFunc("GET /logs", func(w http.ResponseWriter, r *http.Request) {
		where, args := "kind='service_log'", []any{}
		if run := r.URL.Query().Get("run"); run != "" {
			where, args = where+" AND run=?", append(args, run)
		}
		if svc := r.URL.Query().Get("service"); svc != "" {
			where += " AND json_extract(payload,'$.service') = ?"
			args = append(args, svc)
		}
		rows2json(w, `SELECT at, run,
		              json_extract(payload,'$.service')  AS service,
		              json_extract(payload,'$.level')    AS level,
		              json_extract(payload,'$.logger')   AS logger,
		              json_extract(payload,'$.text')     AS text
		              FROM events WHERE `+where+`
		              ORDER BY id DESC LIMIT 500`, args...)
	})

	mux.HandleFunc("GET /judgements", func(w http.ResponseWriter, r *http.Request) {
		rows2json(w, `SELECT * FROM judgements ORDER BY id DESC LIMIT 200`)
	})
	mux.HandleFunc("GET /queue", func(w http.ResponseWriter, r *http.Request) {
		rows2json(w, `SELECT * FROM queue ORDER BY id DESC LIMIT 100`)
	})
	mux.HandleFunc("GET /prices", servePrices)
	mux.HandleFunc("GET /limits", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, loadConfig(confPath))
	})

	serveCharts(mux)
	// The charts are this service's own page now, on its own port. The variable
	// stays so a deployment that still points at a Grafana can, but the default
	// is us.
	serveUI(mux, confPath, env("AMMIT_CHARTS_URL", "/charts"))

	log.Printf("ammit: listening on :%s, limits from %s, db %s%s", port, confPath, dbPath,
		map[bool]string{true: " (dry run)", false: ""}[dryRun])
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

// archive moves finished runs older than retention.days into a file of their
// own, and leaves the live database small.
//
// Nothing is thrown away. A month of runs is a couple of hundred megabytes
// compressed, and an archive is a database like any other: sqlite3 ammit.db
// "ATTACH 'archive/ammit-2026-08.db' AS old" and the old run is back. Deleting
// history to keep a dashboard fast is a trade nobody should have to make.
func archive(conf Config, dbPath string) {
	days, ok := conf.num("retention", "days")
	if !ok {
		return
	}
	cutoff := float64(time.Now().Add(-time.Duration(days*24)*time.Hour).UnixNano()) / 1e9

	mu.Lock()
	defer mu.Unlock()

	var due int
	if err := db.QueryRow(`SELECT count(*) FROM runs WHERE finished IS NOT NULL
	                       AND finished < ?`, cutoff).Scan(&due); err != nil || due == 0 {
		return
	}

	dir := conf.str("retention", "dir", strings.TrimSuffix(dbPath, lastSegment(dbPath))+"archive")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("ammit: no archive directory (%v)", err)
		return
	}
	name := fmt.Sprintf("%s/ammit-%s.db", dir, time.Now().Format("2006-01"))

	if _, err := db.Exec(`ATTACH DATABASE ? AS archive`, name); err != nil {
		log.Printf("ammit: could not open the archive (%v)", err)
		return
	}
	defer db.Exec(`DETACH DATABASE archive`)

	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS archive.runs AS SELECT * FROM runs WHERE 0`,
		`CREATE TABLE IF NOT EXISTS archive.events AS SELECT * FROM events WHERE 0`,
		`CREATE TABLE IF NOT EXISTS archive.judgements AS SELECT * FROM judgements WHERE 0`,
		`CREATE TABLE IF NOT EXISTS archive.limits AS SELECT * FROM limits WHERE 0`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			log.Printf("ammit: archive schema (%v)", err)
			return
		}
	}

	moves := []string{
		`INSERT INTO archive.runs SELECT * FROM runs WHERE finished IS NOT NULL AND finished < ?`,
		`INSERT INTO archive.events SELECT * FROM events WHERE run IN
		 (SELECT run FROM runs WHERE finished IS NOT NULL AND finished < ?)`,
		`INSERT INTO archive.judgements SELECT * FROM judgements WHERE run IN
		 (SELECT run FROM runs WHERE finished IS NOT NULL AND finished < ?)`,
		`DELETE FROM events WHERE run IN
		 (SELECT run FROM runs WHERE finished IS NOT NULL AND finished < ?)`,
		`DELETE FROM judgements WHERE run IN
		 (SELECT run FROM runs WHERE finished IS NOT NULL AND finished < ?)`,
		// The newest row of each limit stays behind whatever its age: it is what
		// the limit is now, and a chart with no starting point draws nothing.
		`INSERT INTO archive.limits SELECT * FROM limits WHERE at < ?
		 AND id NOT IN (SELECT max(id) FROM limits GROUP BY name)`,
		`DELETE FROM limits WHERE at < ?
		 AND id NOT IN (SELECT max(id) FROM limits GROUP BY name)`,
		`DELETE FROM runs WHERE finished IS NOT NULL AND finished < ?`,
	}
	for _, q := range moves {
		if _, err := db.Exec(q, cutoff); err != nil {
			log.Printf("ammit: archiving stopped (%v)", err)
			return
		}
	}
	db.Exec(`VACUUM`)
	log.Printf("ammit: archived %d run(s) older than %.0f days into %s", due, days, name)
}

// safeName keeps a run's own name out of the filesystem's business.
func safeName(s string) string {
	if s == "" {
		return "unnamed"
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	if len(out) > 80 {
		out = out[:80]
	}
	return string(out)
}

func lastSegment(path string) string {
	parts := strings.Split(path, "/")
	return parts[len(parts)-1]
}
