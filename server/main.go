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
	"context"
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

// cutComment splits a line into what it says and the comment after it. A '#'
// only starts a comment at the start of a line or after a space: a command is
// allowed to contain one, and a command that loses half of itself to a comment
// marker is a limit that cannot be enforced.
func cutComment(line string) (string, string) {
	for i := 0; i < len(line); i++ {
		if line[i] != '#' {
			continue
		}
		if i == 0 || line[i-1] == ' ' || line[i-1] == '\t' {
			return line[:i], line[i:]
		}
	}
	return line, ""
}

// unquote takes the quotes off a value that is wrapped in them and leaves every
// other quote where it was. `sh -c "rm -f x"` keeps its own pair: stripped from
// one end only, the command the service runs to stop a run is a syntax error,
// and the run it was meant to stop keeps going.
func unquote(value string) string {
	if len(value) >= 2 {
		first, last := value[0], value[len(value)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return value[1 : len(value)-1]
		}
	}
	return value
}

func loadConfig(path string) Config {
	conf := Config{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return conf
	}
	section := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line, _ = cutComment(line)
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
		conf[section][strings.TrimSpace(key)] = unquote(strings.TrimSpace(value))
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

// handsOff is actions.enforce read as a switch: anything but on, yes, true
// or 1 means the watchdog watches and writes and touches nothing.
func handsOff(c Config) bool {
	switch strings.ToLower(c.str("actions", "enforce", "on")) {
	case "on", "yes", "true", "1":
		return false
	}
	return true
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
	res, err := db.Exec(
		`INSERT INTO events (at, kind, run, phase, session, agent, branch, payload)
		 VALUES (?,?,?,?,?,?,?,?)`,
		at, e.s("kind"), e.s("run"), e.s("phase"), e.s("session"), e.s("agent"),
		e.s("branch"), string(payload))
	if err != nil {
		log.Printf("ammit: could not keep an event: %v", err)
		return
	}
	if id, err := res.LastInsertId(); err == nil {
		lift(id, at, e)
	}
	// A run this store has never heard of gets a row on its first event.
	//
	// The start event travels the same wire as everything else, and that wire
	// blinks: a DNS hiccup at the wrong second dropped one, and a run with no row
	// is a run no limit applies to — invisible to the cost cap, to the deadline,
	// to the queue's idea of what is running. The row is what makes a run real
	// here, so any event may create it and run_start merely fills in the name.
	if run := e.s("run"); run != "" && e.s("kind") != "run_start" {
		db.Exec(`INSERT OR IGNORE INTO runs (run, name, started) VALUES (?,?,?)`,
			run, run, at)
	}

	switch e.s("kind") {
	case "run_start":
		// Tie the queue row to the run it turned into.
		//
		// The queue starts an item and the run is minted afterwards, elsewhere,
		// so the row never learned which run it had become — and run_end closes a
		// queue row by run id, which matched nothing. Four rows sat at "running"
		// with no run against them, and with queue.parallel at one that is a
		// queue that will never start anything again. It had been refusing for
		// three days, twenty-six times over.
		//
		// The oldest running row for this ticket that has not been tied to
		// anything: a queue is a line, and the one at the front is the one that
		// just started.
		db.Exec(`UPDATE queue SET run = ? WHERE id = (
		           SELECT id FROM queue WHERE name = ? AND state = 'running'
		             AND ifnull(run,'') = '' ORDER BY id LIMIT 1)`,
			e.s("run"), e.s("name"))
		// A new run of a ticket supersedes any older run still open under the
		// same name. The two share one directory (/runs/<name>), and stop_run
		// is addressed by name: on 28 August a worker restart orphaned a run
		// mid-flight, its open spans aged in this watchdog for two hours, and
		// the blind stop_run they finally earned touched .cancel into the
		// directory the ticket's NEXT run was nine minutes into using — a dead
		// run's verdict killed the live one. A run that was still open when
		// its ticket started over was not going to finish; say so and close it.
		db.Exec(`UPDATE runs SET finished=?, verdict='BLOCKED',
		         summary='superseded: a newer run of this ticket started'
		         WHERE name=? AND run<>? AND finished IS NULL`,
			at, e.s("name"), e.s("run"))
		// REPLACE, not IGNORE: if an earlier event created the row, this is
		// where it learns the ticket's name and its real start.
		db.Exec(`INSERT OR REPLACE INTO runs (run, name, started) VALUES (?,?,?)`,
			e.s("run"), e.s("name"), at)
	case "flow":
		phases, _ := json.Marshal(e["phases"])
		db.Exec(`INSERT OR REPLACE INTO flows (run, at, mode, phases)
		         VALUES (?,?,?,?)`,
			e.s("run"), at, e.s("mode"), string(phases))
	case "run_end":
		db.Exec(`UPDATE runs SET finished=?, verdict=?, summary=? WHERE run=?`,
			at, e.s("verdict"), e.s("summary"), e.s("run"))
		db.Exec(`UPDATE queue SET state='done', finished=? WHERE run=?`, at, e.s("run"))
		// A run that ended having done nothing.
		//
		// Every rule here is about overshoot — too many turns, too long, too
		// much money — and a run that dies a minute in breaks none of them. It
		// spends nothing. It is, on every number this service watches, an
		// exemplary run. One ended on a NameError ninety seconds after it
		// started, wrote its verdict, and nothing anywhere said a word; it was
		// noticed eleven minutes later by somebody watching a turn counter that
		// was never going to move.
		//
		// Undershoot is the other half of the same job. A run is here to do
		// work, and one that finished without taking a turn did not.
		// In a goroutine, because store() holds mu with a defer and both
		// judgeEmptyRun and judge() take it themselves. Called inline, the first
		// run_end after this shipped locked the watchdog and every event behind
		// it: /runs stopped answering, the deploy refused rather than guess, and
		// nothing was judged again until the container was restarted. A mutex in
		// Go is not reentrant and this file locks in a dozen places — anything
		// called from inside store() must take the lock for itself, later.
		go judgeEmptyRun(e.s("run"), e.s("verdict"), e.s("summary"))
	case "gate":
		// The round is counted here rather than trusted from the caller: a
		// pipeline knows what it found, and this service knows how many times it
		// has been told.
		var round int
		db.QueryRow(`SELECT count(*) FROM gates WHERE run=? AND phase=? AND
		             coalesce(branch,'')=?`, e.s("run"), e.s("phase"),
			e.s("branch")).Scan(&round)
		db.Exec(`INSERT INTO gates (at, run, phase, branch, verdict, findings,
		         round, seconds) VALUES (?,?,?,?,?,?,?,?)`,
			at, e.s("run"), e.s("phase"), e.s("branch"), e.s("verdict"),
			int(e.f("findings")), round+1, e.f("seconds"))
	case "call":
		// The counts are worked out here rather than trusted from the caller:
		// the client knows what it just did, this service knows how often it has
		// been told, and only one of those is a count.
		input, _ := e["input"].(map[string]any)
		kind, target, signature := classify(e.s("tool"), input)
		var repeat, onTarget int
		db.QueryRow(`SELECT count(*) FROM calls WHERE run=? AND signature=?`,
			e.s("run"), signature).Scan(&repeat)
		if target != "" {
			db.QueryRow(`SELECT count(*) FROM calls WHERE run=? AND target=?`,
				e.s("run"), target).Scan(&onTarget)
		}
		ok := 1
		if v, is := e["ok"].(bool); is && !v {
			ok = 0
		}
		db.Exec(`INSERT INTO calls (at, run, phase, branch, agent, session, tool,
		         kind, target, signature, repeat, on_target, seconds, ok, why, request)
		         VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			at, e.s("run"), e.s("phase"), e.s("branch"), e.s("agent"),
			e.s("session"), e.s("tool"), kind, target, signature,
			repeat+1, onTarget+1, e.f("seconds"), ok, e.s("why"), e.s("request"))
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
// A stuck thing is stuck on every tick. Without this, one hung request is a
// judgement every twenty seconds and a worker restarted three times a minute —
// which is how a watchdog turns into the outage. Once per cooldown, then again
// if it is still going.
var judged = map[string]float64{}
var judgedCount = map[string]int{}

// timesJudged is how often this rule has already been applied to this subject.
func timesJudged(rule, subject string) int {
	return judgedCount[rule+"\x1f"+subject]
}

func recently(rule, subject string, cooldown float64) bool {
	if cooldown < 60 {
		cooldown = 60
	}
	now := float64(time.Now().UnixNano()) / 1e9
	key := rule + "\x1f" + subject
	if last, ok := judged[key]; ok && now-last < cooldown {
		return true
	}
	judged[key] = now
	judgedCount[key]++
	return false
}

// Twice a limit is a different event from a limit.
//
// Crossing one is ordinary — an estimate was low, a phase was unlucky, and a
// warning is the right size of response. Crossing it twice over is not the same
// thing happening more: it is a claim that whatever the limit encoded is no
// longer true of this run, and it wants looking at rather than noting. A run
// that took 1600 turns needs a bigger number in the file; a run that took 3200
// needs somebody to read what it was doing.
//
// So a rule may name a second action for its own multiple, and the multiple is
// one setting for all of them.
func escalated(conf Config, rule string, threshold, observed float64,
	action string) (string, bool) {
	factor, ok := conf.num("escalate", "factor")
	if !ok || factor <= 1 || threshold <= 0 || observed < threshold*factor {
		return action, false
	}
	key := "on_" + strings.TrimPrefix(strings.TrimPrefix(rule, "limits."), "timeouts.")
	if over := conf.str("actions", key+"_over", ""); over != "" {
		return over, true
	}
	return conf.str("actions", "on_escalation", action), true
}

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
// endsRun says whether an action leaves the run it was applied to alive.
//
// Restarting a worker does not "fix" a run — it destroys every run that worker
// was carrying, and the row here stays open as though nothing happened. Last
// night that meant one dead run was judged silent fourteen times over four
// hours, each judgement restarting a worker to save a run that the previous
// restart had already killed, until the four-hour timeout finally closed it.
//
// So an action that cannot leave a run running closes the run here, in the
// record, at the moment it is applied. What is listed is a decision, not a
// guess: a pipeline whose "restart" genuinely resumes work says so by leaving
// it out.
func endsRun(conf Config, action string) bool {
	for _, name := range strings.Split(conf.str("actions", "ends_run",
		"stop_run,restart_worker"), ",") {
		if strings.TrimSpace(name) == action && action != "" {
			return true
		}
	}
	return false
}

// {word} and nothing else: a substituted payload carries JSON braces of its
// own — {"key":"APF-1934"} — and those are not holes.
var placeholder = regexp.MustCompile(`\{[a-zA-Z_][a-zA-Z0-9_]*\}`)

// judgeEmptyRun records a run that finished having taken no turns.
//
// Read from the table rather than from the event: the client's run_end carries
// its own count of steps, and the count that matters is what actually arrived
// here. A run whose events never reached this service is exactly the case worth
// catching, and asking the client about it asks the wrong witness.
func judgeEmptyRun(run, verdict, summary string) {
	if run == "" {
		return
	}
	conf := loadConfig(env("AMMIT_CONFIG", "/config/limits.yml"))
	floor, ok := conf.num("limits", "turns_per_run_min")
	if !ok {
		floor = 1
	}
	if floor <= 0 {
		return
	}
	var turns float64
	var started, finished sql.NullFloat64
	mu.Lock()
	db.QueryRow(`SELECT coalesce(turns,0), started, finished FROM runs WHERE run=?`,
		run).Scan(&turns, &started, &finished)
	mu.Unlock()
	if turns >= floor {
		return
	}
	action := conf.str("actions", "on_turns_min", "warn")
	ctx := map[string]string{"run": run, "verdict": verdict, "summary": summary}
	for k, v := range conf["context"] {
		ctx[k] = v
	}
	lived := 0.0
	if started.Valid && finished.Valid {
		lived = finished.Float64 - started.Float64
	}
	judge("run", run, strings.TrimSpace(verdict+" "+summary), "limits.turns_per_run_min",
		floor, turns, action, act(action, conf, ctx))
	log.Printf("ammit: run %s finished after %.0fs having taken %.0f turns: %s",
		run, lived, turns, summary)
}

var reanimations = map[string]int{}

func act(name string, conf Config, ctx map[string]string) string {
	tmpl := conf.str("commands", name, "")
	if tmpl == "" {
		return "no command named " + name
	}
	// A debug run on the subscription is a run the limits must watch and must
	// not touch. actions.enforce: off keeps every judgement and drops every
	// hand - nothing is stopped, retried or restarted until it is on again,
	// and the judgement says so. Starting a run and warning are not hands.
	if handsOff(conf) && name != "start_run" && name != "warn" {
		return "[hands off] " + name + " not run: actions.enforce is off"
	}
	// One cap on reviving, across every kind of retry. A retry that never
	// gives up is the same outage with more billing - 41 restarts in one
	// night on run 96dc986c. The (retry_max+1)th revival of a run is not a
	// revival, it is a run that will not come back: close it instead.
	if name == "retry_session" || name == "retry_branch" ||
		name == "retry_phase" || name == "restart_worker" {
		run := ctx["run"]
		if run == "" {
			run = ctx["name"]
		}
		if run != "" {
			if max, ok := conf.num("limits", "retry_max"); ok && max > 0 {
				reanimations[run]++
				if float64(reanimations[run]) > max {
					finish(run, "BLOCKED", fmt.Sprintf(
						"revived %d times and still not reporting - past "+
							"limits.retry_max (%.0f); closed rather than "+
							"revived again", reanimations[run]-1, max))
					return fmt.Sprintf("closed: retry_max %.0f reached", max)
				}
			}
		}
	}
	for key, value := range ctx {
		tmpl = strings.ReplaceAll(tmpl, "{"+key+"}", value)
	}
	// A hole nobody filled is not a command.
	//
	// The session-timeout branch set ctx["session"] and never ctx["agent"], so
	// retry_session went out asking the worker to retry a session literally
	// named "{agent}". It matched nothing — "retry session {agent} -> 0 wait(s)"
	// — ten times on APF-1934, and the record said the action had been applied.
	// A limit that fires and does nothing is worse than one that does not fire:
	// the row says it was handled.
	//
	// This was found and fixed once already, for requests; the comment saying so
	// sits forty lines above the branch where it survived. So the check belongs
	// here, once, rather than in each branch that has to remember.
	if hole := placeholder.FindString(tmpl); hole != "" {
		return "refused: " + hole + " was never filled — " + name +
			" would have been a no-op"
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
	now := float64(time.Now().UnixNano()) / 1e9
	db.Exec(`UPDATE runs SET finished=?, verdict=?, summary=? WHERE run=?`,
		now, verdict, summary, run)
	db.Exec(`UPDATE queue SET state='done', finished=? WHERE run=?`, now, run)
	mu.Unlock()
	closeSpansOf(run, now, "the run ended")
}

// closeSpansOf writes the closing events for everything still open under a run
// that is over.
//
// A run does not end tidily. It is stopped from outside, or its worker is
// restarted under it, and whatever was in flight at that moment — a request, a
// session, a phase, a deploy — never sends the event that would close it. Left
// alone those spans read as still running, for ever: the charts draw them
// growing, and anything asking "what is open" gets an answer about a run that
// finished last night. Two of them have been sitting in this database since a
// three-second restart yesterday.
//
// A span cannot outlive its run. So the run's end is their end, recorded as
// what it is — closed because the run ended, not because the work finished —
// and never mistaken for a clean one: ok is false and the reason says who did
// it.
// notClosed drops the spans ammit closed on a run's behalf, unless asked for.
// They carry an age where a duration belongs and it is measured in hours.
func notClosed(r *http.Request) string {
	if r.URL.Query().Get("closed") != "" {
		return ""
	}
	return " AND ifnull(json_extract(payload,'$.error'),'') NOT LIKE 'closed by ammit%'"
}

func closeSpansOf(run string, at float64, why string) {
	pairs := []struct{ start, end, column string }{
		{"request_start", "request_end", "session"},
		{"item_start", "item_end", "session"},
		// Keyed on the session, not on the agent: a fan-out runs seven
		// branches of one agent at once, and under the agent's name they are a
		// single span opening seven times. The client sends agent@branch.
		{"session_start", "session_end", "session"},
		{"phase_start", "phase_end", "phase"},
	}
	for _, p := range pairs {
		for name, age := range openSpans(run, p.start, p.end, p.column) {
			e := event{"kind": p.end, "at": at, "run": run, p.column: name,
				"ok": false, "seconds": age,
				"error": "closed by ammit: " + why}
			if p.column != "session" {
				e["session"] = name
			}
			store(e)
		}
	}
}

type openRun struct {
	run, name    string
	started, usd float64
	turns        int
}

// sweepAbandoned closes runs nothing has reported on for longer than a run is
// allowed to take.
//
// Every rule works from openRuns, which asks for runs started in the last
// seventy-two hours. That window was meant to keep the loop off ancient rows,
// and it also means a row still open after seventy-two hours can never be
// closed by anything: not judged, not timed out, not swept. Twenty of them were
// sitting in this database, one genuinely open and nineteen that had ended
// without the event that says so.
//
// Silence is the signal, not age. A run reports constantly while it lives — a
// turn, a call, a phase — so nothing heard for longer than timeouts.run means
// the run is not running, whether it was killed, whether its worker was
// replaced, or whether this service was down when it ended. It is closed as
// what it is: abandoned, not finished, and never confused with a verdict
// somebody's work produced.
// sweepQueue closes queue rows whose run is over, and rows that never became a
// run at all.
//
// A row at "running" holds a slot for ever, and the slot is the whole of what
// queue.parallel controls. Two ways it happens: the run it became has finished
// and the row was never tied to it, or the start failed and nothing ever came
// back. Both look the same from here and both end the same way.
func sweepQueue(conf Config) {
	mu.Lock()
	defer mu.Unlock()
	now := float64(time.Now().UnixNano()) / 1e9
	// Tied to a run that is over.
	db.Exec(`UPDATE queue SET state='done', finished=?
	         WHERE state='running' AND ifnull(run,'') <> ''
	           AND run IN (SELECT run FROM runs WHERE finished IS NOT NULL)`, now)
	// Never tied to anything, and older than a run is allowed to take. Whatever
	// it was, it is not running now.
	if limit, ok := conf.num("timeouts", "run"); ok && limit > 0 {
		db.Exec(`UPDATE queue SET state='done', finished=?
		         WHERE state='running' AND ifnull(run,'') = ''
		           AND coalesce(started, requested) < ?`, now, now-limit)
	}
}

func sweepAbandoned(conf Config) {
	limit, ok := conf.num("timeouts", "run")
	if !ok || limit <= 0 {
		return
	}
	mu.Lock()
	rows, err := db.Query(`SELECT r.run, coalesce(max(e.at), r.started)
	                       FROM runs r LEFT JOIN events e ON e.run = r.run
	                       WHERE r.finished IS NULL GROUP BY r.run`)
	type quiet struct {
		run string
		at  float64
	}
	var found []quiet
	if err == nil {
		for rows.Next() {
			var q quiet
			if rows.Scan(&q.run, &q.at) == nil {
				found = append(found, q)
			}
		}
		rows.Close()
	}
	mu.Unlock()
	now := float64(time.Now().UnixNano()) / 1e9
	for _, q := range found {
		silent := now - q.at
		if silent <= limit {
			continue
		}
		finish(q.run, "ABANDONED", fmt.Sprintf(
			"nothing has reported on this run for %.0f minutes, which is longer "+
				"than timeouts.run — closed as abandoned, not as finished",
			silent/60))
	}
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
// lastBranch is the branch of the newest event of this run, or "" if this run
// never fanned out.
func lastBranch(run string) string {
	mu.Lock()
	defer mu.Unlock()
	var branch string
	db.QueryRow(`SELECT coalesce(branch,'') FROM events WHERE run=? AND ifnull(branch,'') <> ''
	             ORDER BY id DESC LIMIT 1`, run).Scan(&branch)
	return branch
}

// afterTool says whether this wait is one the agent caused by asking for a
// tool. The session logs the tool call, then waits for it to finish, so the
// newest thing said by this session before the wait began settles it.
func afterTool(run, request string) bool {
	mu.Lock()
	defer mu.Unlock()
	var started float64
	var agent, wait string
	if err := db.QueryRow(`SELECT at, coalesce(agent,''),
	                       coalesce(json_extract(payload,'$.wait'),'')
	                       FROM events
	                       WHERE run=? AND kind='request_start' AND session=?
	                       ORDER BY id DESC LIMIT 1`, run, request).
		Scan(&started, &agent, &wait); err != nil {
		return false
	}
	// The runner says what the wait covers, when it says anything: "tool"
	// spans a tool's execution and gets the browser-suite hour, "model" means
	// the tools are settled and only the answer is outstanding - which has no
	// business taking ten minutes. The log-level guess below misread exactly
	// that case: the runner logs a tool's RESULT at level tool too, so a hung
	// model stream right after a delivered result sat under the hour ceiling
	// for thirteen minutes on run 0337ed5e.
	if wait == "tool" || wait == "model" {
		return wait == "tool"
	}
	var level string
	if err := db.QueryRow(`SELECT coalesce(json_extract(payload,'$.level'),'')
	                       FROM events WHERE run=? AND kind='log' AND agent=? AND at<=?
	                       ORDER BY id DESC LIMIT 1`, run, agent, started).
		Scan(&level); err != nil {
		return false
	}
	return level == "tool"
}

// The timeout names this service owns. A client cannot claim one of these as a
// kind, because "my module took longer than timeouts.run" is not a sentence
// anyone wants to debug.
var reserved = map[string]bool{
	"run": true, "phase": true, "session": true, "request": true,
	"request_tool": true, "turn": true,
}

// spanFacts is who opened this wait, on which branch, and when.
func spanFacts(run, request string) (string, string, float64) {
	mu.Lock()
	defer mu.Unlock()
	var at float64
	var agent, branch string
	db.QueryRow(`SELECT at, coalesce(agent,''), coalesce(branch,'')
	             FROM events WHERE run=? AND kind='request_start' AND session=?
	             ORDER BY id DESC LIMIT 1`, run, request).Scan(&at, &agent, &branch)
	return agent, branch, at
}

// spanIsOrphaned says whether this request belongs to a session that has since
// ended — which makes it a lost event rather than a hanging call.
func spanIsOrphaned(run, request string) bool {
	agent, branch, at := spanFacts(run, request)
	if agent == "" {
		return false
	}
	mu.Lock()
	defer mu.Unlock()
	var ended int
	db.QueryRow(`SELECT count(*) FROM events WHERE run=? AND kind='session_end'
	             AND coalesce(agent,'')=? AND coalesce(branch,'')=? AND at>=?`,
		run, agent, branch, at).Scan(&ended)
	return ended > 0
}

// itemFacts is what an open unit of work said about itself when it started:
// what kind of thing it is, and who is running it.
func itemFacts(run, item string) (string, string) {
	mu.Lock()
	defer mu.Unlock()
	var kind, agent string
	// itemkind, not kind: `kind` on an event is already the event's own name, so
	// what sort of unit this is has to travel under a name of its own.
	db.QueryRow(`SELECT coalesce(json_extract(payload,'$.itemkind'),''), coalesce(agent,'')
	             FROM events WHERE run=? AND kind='item_start' AND session=?
	             ORDER BY id DESC LIMIT 1`, run, item).Scan(&kind, &agent)
	return kind, agent
}

// turnsPerSession is how many turns each agent has taken in the session it has
// open now — not across the run, which is a different question with a different
// answer when an agent runs once per branch.
func turnsPerSession(run string) map[string]float64 {
	mu.Lock()
	defer mu.Unlock()
	rows, err := db.Query(`
		SELECT coalesce(t.agent,''), count(*) FROM events t
		WHERE t.run=? AND t.kind='turn' AND ifnull(t.agent,'') <> ''
		  AND t.at >= coalesce((SELECT max(s.at) FROM events s
		      WHERE s.run=t.run AND s.kind='session_start'
		        AND coalesce(s.agent,'')=coalesce(t.agent,'')), 0)
		GROUP BY 1`, run)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var who string
		var n float64
		if err := rows.Scan(&who, &n); err == nil {
			out[who] = n
		}
	}
	return out
}

// spinningSessions is every open session that keeps asking the model and has
// not touched a tool for a whole window: at least minRequests request_starts
// in the trailing window seconds and zero tool calls in the same window.
//
// This is the shape a turn count cannot see. The session that motivated it
// made ninety-seven requests in ten minutes, seventy-two returning nothing,
// with no tool call and no error anywhere — formally nothing was wrong, so no
// watchdog reacted, and the phase waited on it while thirteen sibling branches
// had finished. The two signals together are what tells it from honest work:
// a session composing one long answer makes a handful of requests, and a
// session grinding through a suite makes calls — measured across a whole
// four-hour run, no healthy session ever crossed both lines at once.
//
// Returns session key -> requests in the window.
func spinningSessions(run string, window, minRequests float64) map[string]float64 {
	mu.Lock()
	defer mu.Unlock()
	since := float64(time.Now().UnixNano())/1e9 - window
	rows, err := db.Query(`
		SELECT s.session,
		  (SELECT count(*) FROM events r WHERE r.run=s.run AND r.kind='request_start'
		     AND coalesce(r.agent,'')=s.agent AND coalesce(r.branch,'')=s.branch
		     AND r.at>=?) reqs,
		  (SELECT count(*) FROM events c WHERE c.run=s.run AND c.kind='call'
		     AND coalesce(c.session,'')=s.session AND c.at>=?) calls
		FROM (SELECT run, coalesce(session,'') session, coalesce(agent,'') agent,
		             coalesce(branch,'') branch
		      FROM events WHERE run=? AND kind='session_start'
		        AND coalesce(session,'')<>''
		        AND coalesce(session,'') NOT IN (
		            SELECT coalesce(session,'') FROM events
		            WHERE run=? AND kind='session_end')
		      GROUP BY 2) s`, since, since, run, run)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var session string
		var reqs, calls float64
		if err := rows.Scan(&session, &reqs, &calls); err == nil &&
			calls == 0 && reqs >= minRequests {
			out[session] = reqs
		}
	}
	return out
}

// heaviestTurn is the largest single prompt this run has sent recently, and who
// sent it. Recently rather than ever, because a run is judged on what it is
// doing now: one heavy turn an hour ago is history, and a limit that keeps
// firing on history cannot be acted on.
func heaviestTurn(run string) (float64, string, string) {
	mu.Lock()
	defer mu.Unlock()
	since := float64(time.Now().Add(-15*time.Minute).UnixNano()) / 1e9
	var size float64
	var who, phase string
	db.QueryRow(`SELECT coalesce(json_extract(payload,'$.context'),0),
	                    coalesce(agent,'?'), coalesce(phase,'')
	             FROM events WHERE run=? AND kind='turn' AND at>=?
	             ORDER BY coalesce(json_extract(payload,'$.context'),0) DESC
	             LIMIT 1`, run, since).Scan(&size, &who, &phase)
	return size, who, phase
}

// workerBusy asks the machine, not the run: is the worker container visibly
// burning CPU right now? A branch suite is legally silent for ten minutes -
// one behave process, browsers rendering, nothing to report - and the
// run-silence judgement read that honest quiet as a hang. The watched must
// not testify about its own pulse (a self-reported heartbeat is the same
// lie in the other direction); the watchdog's own sampler already measures
// the container every minute, so silence plus a busy machine is work, and
// silence plus an idle machine is the hang this timeout exists for.
func workerBusy(conf Config) bool {
	floor, ok := conf.num("limits", "quiet_cpu")
	if !ok || floor <= 0 {
		return false
	}
	worker := conf.str("context", "worker", "")
	if worker == "" {
		return false
	}
	mu.Lock()
	defer mu.Unlock()
	var at, cpu float64
	err := db.QueryRow(`SELECT at, coalesce(json_extract(payload,'$.cpu_pct'),0)
	                    FROM events WHERE kind='sample'
	                      AND json_extract(payload,'$.container')=?
	                    ORDER BY id DESC LIMIT 1`, worker).Scan(&at, &cpu)
	if err != nil {
		return false
	}
	if float64(time.Now().UnixNano())/1e9-at > 180 {
		return false
	}
	return cpu >= floor
}

// workerUp asks the docker daemon whether the worker container is running.
// Best effort with a short leash: a daemon that cannot answer in five seconds
// is treated as "unknown", and unknown keeps the certificate unsigned - the
// caller then leaves the row open, which is the recoverable mistake.
func workerUp(conf Config) bool {
	worker := conf.str("context", "worker", "")
	if worker == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f",
		"{{.State.Running}}", worker).Output()
	if err != nil {
		return true // unknown: do not certify a death the daemon cannot confirm
	}
	return strings.TrimSpace(string(out)) == "true"
}

// heardFrom is seconds since the WORKER said anything at all - any event it
// sent, agented or not; only the watchdog's own instruments are excluded.
func heardFrom(run string) float64 {
	mu.Lock()
	defer mu.Unlock()
	var at float64
	// heartbeat excluded alongside the watchdog's own instruments: it is the
	// process pulse, and a 60-second pulse would otherwise reset this
	// work-silence measure forever, hiding a wedged session behind a living
	// loop. Work-silence and process-pulse are two questions with two checks.
	err := db.QueryRow(`SELECT at FROM events
	                    WHERE run=? AND kind NOT IN ('sample','netprobe','heartbeat')
	                    ORDER BY id DESC LIMIT 1`, run).Scan(&at)
	if err != nil {
		return 0
	}
	return float64(time.Now().UnixNano())/1e9 - at
}

// heartbeatAge is seconds since the runner PROCESS last said it is looping,
// or -1 when it has never sent a heartbeat (an old run, or one that died
// before the first pulse - do not judge a pulse that never started). This is
// the fast reaper run 14 lacked: the worker was recreated and no work event
// came, but nothing watched the pulse because the pulse was not an event.
func heartbeatAge(run string) float64 {
	mu.Lock()
	defer mu.Unlock()
	var at float64
	err := db.QueryRow(`SELECT at FROM events
	                    WHERE run=? AND kind='heartbeat'
	                    ORDER BY id DESC LIMIT 1`, run).Scan(&at)
	if err != nil {
		return -1
	}
	return float64(time.Now().UnixNano())/1e9 - at
}

func quietFor(run string) (float64, string, string) {
	mu.Lock()
	defer mu.Unlock()
	var at float64
	var agent, phase string
	// The pulse reads only what an AGENT said. Excluding the watchdog's own
	// instruments (samples, probes) was round one; round two was a service_log
	// line - the control's own "retry -> 0 waits" bookkeeping - becoming the
	// run's newest row, so ten minutes later the silence was judged with an
	// empty agent and the BLIND branch shot the run (8c7c591f, minute 161).
	// Any agentless row can only ever blind the aim or fake the pulse, so
	// none of them count: silence is measured from the last thing a session
	// actually said, and the aim lands on that session. A run with no agented
	// events yet has no pulse to judge - better no judgement than a blind one.
	err := db.QueryRow(`SELECT at, coalesce(agent,''), coalesce(phase,'') FROM events
	                    WHERE run=? AND coalesce(agent,'') != ''
	                    ORDER BY id DESC LIMIT 1`, run).Scan(&at, &agent, &phase)
	if err != nil {
		return 0, "", ""
	}
	return float64(time.Now().UnixNano())/1e9 - at, agent, phase
}

// openSpans is what this run has begun and not yet finished, by name.
//
// Asked of the database rather than replayed in here. Replaying meant reading
// every request this run had ever made — fifteen thousand rows, every tick, to
// find the two that were still open — and doing it again for phases and again
// for sessions. The answer was always "the ones with no end", which is a
// sentence SQL can say by itself.
// openPhases is the phase this run is in and how long it has been in it.
//
// Derived, not reported. Every event already carries the phase it happened in —
// that is what the charts are coloured by — so a phase begins at its first event
// and ends at its phase_end, and asking the pipeline to send a phase_start as
// well would be asking it to repeat something it has already said 15,000 times.
// The pair-of-events version never worked precisely because nothing sent the
// first half, and timeouts.phase had quietly never fired.
func openPhases(run string) map[string]float64 {
	mu.Lock()
	defer mu.Unlock()
	// Only a phase somebody OPENED counts as open. Any event carries a phase
	// column — an orchestrator's deploy step logs under phase "envdeploy"
	// without ever starting a phase of that name — and counting those held a
	// phantom phase open for a whole run: the deploy finished in fourteen
	// seconds, the "phase" aged past its limit twice, and stop_phase went out
	// against a phase no worker had, twice, as a no-op.
	rows, err := db.Query(`
		SELECT coalesce(phase,''), min(at) FROM events
		WHERE run=? AND kind='phase_start' AND ifnull(phase,'') <> ''
		  AND coalesce(phase,'') NOT IN (
		      SELECT coalesce(phase,'') FROM events WHERE run=? AND kind='phase_end')
		GROUP BY 1`, run, run)
	if err != nil {
		return nil
	}
	defer rows.Close()
	now := float64(time.Now().UnixNano()) / 1e9
	live := map[string]float64{}
	for rows.Next() {
		var phase string
		var at float64
		if err := rows.Scan(&phase, &at); err == nil {
			live[phase] = now - at
		}
	}
	return live
}

// sessionQuiet is how long a session has said nothing: seconds since its last
// event of any kind - the session key on a call, or the agent and branch
// columns a request or a log line carries. Returns -1 when nothing matches,
// so the caller can fall back to the span's age rather than judging silence
// nobody measured.
func sessionQuiet(run, session string) float64 {
	agent, branch := session, ""
	if at := strings.Index(session, "@"); at >= 0 {
		agent, branch = session[:at], session[at+1:]
	}
	mu.Lock()
	defer mu.Unlock()
	var last float64
	err := db.QueryRow(`SELECT max(at) FROM events
	                    WHERE run=? AND (session=?
	                       OR (coalesce(agent,'')=? AND coalesce(branch,'')=?))`,
		run, session, agent, branch).Scan(&last)
	if err != nil || last == 0 {
		return -1
	}
	return float64(time.Now().UnixNano())/1e9 - last
}

func openSpans(run, startKind, endKind, column string) map[string]float64 {
	mu.Lock()
	defer mu.Unlock()
	rows, err := db.Query(fmt.Sprintf(`
		SELECT coalesce(%[1]s,''), max(at) FROM events
		WHERE run=? AND kind=? AND ifnull(%[1]s,'') <> ''
		  AND coalesce(%[1]s,'') NOT IN (
		      SELECT coalesce(%[1]s,'') FROM events WHERE run=? AND kind=?)
		GROUP BY 1`, column), run, startKind, run, endKind)
	if err != nil {
		return nil
	}
	defer rows.Close()
	now := float64(time.Now().UnixNano()) / 1e9
	live := map[string]float64{}
	for rows.Next() {
		var key string
		var at float64
		if err := rows.Scan(&key, &at); err == nil {
			live[key] = now - at
		}
	}
	return live
}

// limitsSeen is the last value written for each limit, so the table grows when
// something changes rather than on every tick.
var limitsSeen = map[string]struct{ value, at float64 }{}

// recordLimits writes the limits down as a series, so a chart can draw the line
// a run was measured against instead of a number somebody typed into a panel
// months ago. A limit edited mid-run bends its own line at the minute it was
// edited, and the run underneath it is right there to compare.
//
// Every numeric setting is recorded, whatever it is called: this service does
// not get to decide which of somebody's limits are the interesting ones.
func recordLimits(conf Config) {
	now := float64(time.Now().UnixNano()) / 1e9
	for section, kv := range conf {
		switch section {
		case "commands", "actions", "context":
			continue // names of things to run, not numbers to cross
		}
		for key, raw := range kv {
			value, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				continue
			}
			name := section + "." + key
			// A heartbeat every minute as well as on change: a line needs two
			// points, and a chart of the last hour should not be empty because
			// nobody touched the config today.
			if last, ok := limitsSeen[name]; ok && last.value == value && now-last.at < 60 {
				continue
			}
			mu.Lock()
			_, err = db.Exec("INSERT INTO limits (at, name, value) VALUES (?,?,?)",
				now, name, value)
			mu.Unlock()
			if err != nil {
				log.Printf("ammit: could not record limit %s: %v", name, err)
				continue
			}
			limitsSeen[name] = struct{ value, at float64 }{value, now}
		}
	}
}

func weigh(conf Config) {
	for _, r := range openRuns() {
		age := float64(time.Now().UnixNano())/1e9 - r.started
		// The fast pulse check, ahead of the slow work-silence one. The runner
		// posts a heartbeat every minute; timeouts.heartbeat (120s) is two
		// missed in a row, which is the process wedged or gone - reanimate it
		// with restart_worker, and act()'s retry_max cap turns the third dead
		// pulse into a close instead of a fourth restart. Skipped when there
		// is no heartbeat yet (-1): a pulse that never started is not a pulse
		// that stopped, and the worker_gone/orphan path below still catches
		// the run that died before its first beat.
		if hb, ok := conf.num("timeouts", "heartbeat"); ok && hb > 0 {
			if pulse := heartbeatAge(r.run); pulse >= 0 && pulse > hb {
				action := conf.str("actions", "on_heartbeat_gone", "restart_worker")
				ctx := map[string]string{"run": r.run, "name": r.name,
					"branch": lastBranch(r.run)}
				for k, v := range conf["context"] {
					ctx[k] = v
				}
				judge("run", r.run, r.name, "timeouts.heartbeat", hb, pulse,
					action, act(action, conf, ctx))
				continue
			}
		}
		// A run whose worker has stopped reporting entirely is not a run to
		// act on - it is a corpse to close. The worker heartbeats every minute
		// while it is alive, so a run this quiet has no process behind it: a
		// stack restart, a kill -9, a hard stop. Judging its orphaned spans
		// bought two hours of no-op retries on one ghost, and the blind
		// stop_run it finally earned touched .cancel into the directory the
		// ticket's NEXT run was using. No commands for the dead: close the
		// row, say why, move on. Zero disables, like every limit here.
		if gone, ok := conf.num("timeouts", "worker_gone"); ok && gone > 0 && age > gone {
			// A different ear from the turn-silence one, on purpose. The
			// turn judgement listens for AGENTS (an agentless row must not
			// blind its aim); this one asks whether the worker PROCESS is
			// alive at all, and for that any event the worker itself sent
			// counts - a service_log line, an item boundary, a heartbeat.
			// Feeding it the agented-only pulse turned a behave module's
			// forty legal silent minutes into a corpse: run c306ccfe was
			// closed as "worker gone" four minutes after its own control
			// chatter, mid-suite, mid-frame. Only ammit's own instruments
			// (samples, probes) stay excluded - they prove ammit is alive,
			// not the worker.
			if quiet := heardFrom(r.run); quiet > gone {
				// Ask the docker daemon before signing the certificate. Run
				// 46a57530 sat in a real fifteen-minute wedge - a session
				// resume hung with no event of any kind - and came back to
				// life four seconds after this closed its row. A container
				// the daemon calls running is a wedge, not a corpse: leave
				// the row open so the wedge stays judged (the turn timeout
				// aims and retries it); close only what is genuinely not
				// there any more.
				// A recreated worker under a live run - the orphan case -
				// is caught earlier and faster by the heartbeat check at the
				// top of this loop: the fresh empty container sends no
				// heartbeat for the run, so on_heartbeat_gone fires at 120s
				// and closes it within a few restarts, long before this. What
				// is left here is the genuine wedge: a running container that
				// IS still heartbeating but whose work has gone quiet past
				// worker_gone. Leave it - the turn timeout aims and retries,
				// and retry_max ends that if it never wakes.
				if workerUp(conf) {
					continue
				}
				finish(r.run, "BLOCKED", fmt.Sprintf(
					"ammit: worker gone - nothing heard for %.0fs", quiet))
				continue
			}
		}
		ctx := map[string]string{"run": r.run, "name": r.name,
			// Which fan-out branch the run was last heard from, so a command can
			// name one branch instead of taking the whole run down with it.
			"branch": lastBranch(r.run)}
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
		// Money as it arrives, not only as it is billed. The SDK's bill comes
		// at session end and never for a session this service stops; the turns
		// come as they happen and the catalog prices them. The larger of the
		// two is the run's spend: the bill where it has come, the count where
		// it has not. Judged on the bill alone, eighteen stopped sessions of
		// run f4a30b19 cost nothing.
		if limit, ok := conf.num("limits", "usd_per_run"); ok {
			if usd := spentUSD(r); usd > limit {
				action := conf.str("actions", "on_usd", "stop_run")
				judge("run", r.run, r.name, "limits.usd_per_run", limit, usd, action,
					act(action, conf, ctx))
				finish(r.run, "BLOCKED", "ammit: over limits.usd_per_run")
				continue
			}
		}
		// What the pipeline is carrying, turn after turn.
		//
		// One number, not two. An average per session is this number multiplied
		// by the turns the session took and divided by them again — a third
		// figure derived from two that are already here, arriving only once the
		// session is over, which is after every turn in it has been paid for.
		//
		// A cost limit says a run spent too much and a turn limit says it took
		// too many; neither says the prompt was three times the size it needed
		// to be, which is the cheapest thing to fix and the easiest to not
		// notice. One run carried a 253 KB document in every message: 27 million
		// tokens re-sent, a quarter of the run, invisible in both the dollars and
		// the turns because it looked exactly like ordinary work.
		//
		// Measured per session as the context each of its turns carried, which is
		// what a prompt costs when it is sent again and again.
		// One turn, as it was sent. The per-session average below says a session
		// was heavy after it has finished; this says a turn is heavy while the
		// next one has not been paid for yet.
		if limit, ok := conf.num("limits", "turn_tokens"); ok && limit > 0 {
			if size, who, phase := heaviestTurn(r.run); size > limit &&
				!recently("limits.turn_tokens", r.run+who, 900) {
				action := conf.str("actions", "on_turn_tokens", "warn")
				ctx["agent"], ctx["phase"] = who, phase
				judge("turn", r.run, who, "limits.turn_tokens", limit, size,
					action, act(action, conf, ctx))
			}
		}
		// One session's turns. The run-wide count above cannot see this: a fan-out
		// of twenty-three branches made 1336 turns between them and every session
		// stayed under its own ceiling while the run stayed under its own, so
		// nothing anywhere had an opinion about the middle.
		if limit, ok := conf.num("limits", "turns_per_session"); ok && limit > 0 {
			for who, turns := range turnsPerSession(r.run) {
				if turns <= limit || recently("limits.turns_per_session", r.run+who, 900) {
					continue
				}
				action := conf.str("actions", "on_session_turns", "warn")
				ctx["agent"] = who
				judge("session", r.run, who, "limits.turns_per_session", limit, turns,
					action, act(action, conf, ctx))
				if endsRun(conf, action) {
					finish(r.run, "BLOCKED", fmt.Sprintf(
						"ammit: %s took %.0f turns in one session", who, turns))
					break
				}
			}
		}
		// The spinner the turn count cannot see: requests piling up, no tool
		// touched. on_session_turns went back to warn because restarting on
		// the count restarted the sessions that needed the turns; this reads
		// the signal that change was actually aimed at. Absent or zero window
		// switches it off, like every limit whose zero is a decision.
		if window, ok := conf.num("limits", "spin_window"); ok && window > 0 {
			minreq, ok := conf.num("limits", "spin_requests")
			if !ok || minreq <= 0 {
				minreq = 30
			}
			for session, reqs := range spinningSessions(r.run, window, minreq) {
				if recently("limits.session_spin", r.run+session, window) {
					continue
				}
				action := conf.str("actions", "on_session_spin", "retry_session")
				// The full session key as {agent}, same addressing as the
				// session timeout above: the worker's control handle accepts
				// agent@branch and ends exactly this branch's session.
				ctx["session"] = session
				ctx["agent"], ctx["branch"] = session, ""
				if at := strings.Index(session, "@"); at >= 0 {
					ctx["branch"] = session[at+1:]
				}
				judge("session", r.run, session, "limits.session_spin", minreq,
					reqs, action, act(action, conf, ctx))
				if endsRun(conf, action) {
					finish(r.run, "BLOCKED", fmt.Sprintf(
						"ammit: %s made %.0f requests in %.0fs without touching "+
							"a tool", session, reqs, window))
					break
				}
			}
		}
		if limit, ok := conf.num("limits", "turns_per_run"); ok && float64(r.turns) > limit {
			action := conf.str("actions", "on_turns", "warn")
			action, over := escalated(conf, "limits.turns_per_run", limit,
				float64(r.turns), action)
			if !recently("limits.turns_per_run", r.run+action, 1800) {
				rule := "limits.turns_per_run"
				if over {
					rule += " (twice over)"
				}
				judge("run", r.run, r.name, rule, limit, float64(r.turns),
					action, act(action, conf, ctx))
			}
		}

		if limit, ok := conf.num("timeouts", "turn"); ok {
			// The same run, judged silent again and again, is one fault reported
			// many times. Fourteen judgements in one night, each restarting a
			// worker that could not resume the run it had killed, and the run's
			// row stayed open through all of it — so the cooldown grew to the
			// gap between them and nothing changed. Twice, then leave it: if the
			// action was going to work it worked, and if it was not, repeating it
			// is only more damage.
			if quiet, agent, phase := quietFor(r.run); quiet > limit &&
				!workerBusy(conf) &&
				timesJudged("timeouts.turn", r.run) < 2 &&
				!recently("timeouts.turn", r.run, limit) {
				// When a whole run goes quiet, the last thing that spoke is the
				// thing that stopped speaking — so this can be aimed at that one
				// session rather than at the container around it. Restarting the
				// container was the old answer, and it treats one wedged agent by
				// killing every branch beside it and the run they belong to.
				action := conf.str("actions", "on_turn_timeout", "warn")
				ctx["agent"], ctx["phase"] = agent, phase
				if agent == "" {
					// Nothing to aim at: whatever spoke last did not name an
					// agent. Then, and only then, the blunt instrument.
					action = conf.str("actions", "on_turn_timeout_blind", "restart_worker")
				}
				judge("turn", r.run, strings.TrimSpace(agent+" "+phase), "timeouts.turn",
					limit, quiet, action, act(action, conf, ctx))
				if endsRun(conf, action) {
					finish(r.run, "BLOCKED", fmt.Sprintf(
						"ammit: silent for %.0fs, and %s ends the run", quiet, action))
					continue
				}
			}
		}
		// One request to the model, timed from out here. Inside the run this is
		// invisible: a call that never comes back has no error to log and no
		// retry to trigger, and the two-hour silence that cost a run its
		// afternoon looked exactly like a long think.
		if limit, ok := conf.num("timeouts", "request"); ok {
			// Two limits, because a wait is two different things wearing one
			// name. Between "asked the model" and "the model answered" nothing
			// observed here has ever taken three minutes. But the same gap also
			// covers a tool the agent asked for, and one of those tools runs a
			// browser through a suite for twenty minutes — cut that at the
			// model's limit and the watchdog is breaking the work it guards.
			//
			// Which one this is comes from what the session said last: a tool
			// call is logged before the wait it causes.
			tool, hasTool := conf.num("timeouts", "request_tool")
			for request, age := range openSpans(r.run, "request_start", "request_end", "session") {
				// A wait inside a session that has already ended is not a wait.
				//
				// Events are sent best-effort — a pipeline must not stop because
				// its bookkeeping is down — so a server that blinks loses some,
				// and the one whose loss matters is the one that closes a span.
				// It stays open for ever after, and this service dutifully asks
				// for a restart of a session that finished an hour ago. Twice
				// tonight, on a run that had no long wait at all: the longest was
				// 98 seconds.
				//
				// The record already answers it. A session says when it ended,
				// under the same agent and branch the wait was opened with.
				if spanIsOrphaned(r.run, request) {
					continue
				}
				rule, against := "timeouts.request", limit
				if hasTool && afterTool(r.run, request) {
					rule, against = "timeouts.request_tool", tool
				}
				if against <= 0 || age <= against || recently(rule, request, against) {
					continue
				}
				limit = against
				action := conf.str("actions", "on_request_timeout", "restart_worker")
				// Who to aim at, not just what is late. Without this the command
				// went out with "{agent}" in it, unsubstituted, and asked the
				// worker to retry a session by that literal name — which matched
				// nothing, so a real hang would have been answered with a no-op.
				who, wbr, _ := spanFacts(r.run, request)
				// The full session key, not the bare agent: fourteen branches
				// each run an agent called "implement", and a bare name asks
				// the worker to end every one of them for one branch's hang.
				if wbr != "" {
					who = who + "@" + wbr
				}
				ctx["request"], ctx["agent"] = request, who
				judge("request", r.run, request, rule, against, age,
					action, act(action, conf, ctx))
				if endsRun(conf, action) {
					finish(r.run, "BLOCKED", fmt.Sprintf(
						"ammit: a request ran %.0fs, and %s ends the run", age, action))
					break
				}
			}
		}
		// A test and a test module are different sizes of the same thing, and a
		// pipeline that cannot bound them separately ends up bounding neither:
		// one scenario stuck on a locator looks nothing like a whole .feature
		// grinding through forty of them, and the number that catches the first
		// would cut the second in half.
		//
		// What a "module" is belongs to the stack, not to this service: a
		// .feature here, a test file elsewhere, a class somewhere else. The
		// client says what kind of thing it started and the limit is looked up
		// under that name, so timeouts.test and timeouts.module are simply the
		// two kinds that most stacks have.
		for item, age := range openSpans(r.run, "item_start", "item_end", "session") {
			kind, agent := itemFacts(r.run, item)
			if kind == "" || reserved[kind] {
				continue
			}
			against, ok := conf.num("timeouts", kind)
			if !ok || against <= 0 || age <= against || recently("timeouts."+kind, item, against) {
				continue
			}
			action := conf.str("actions", "on_"+kind+"_timeout",
				conf.str("actions", "on_item_timeout", "warn"))
			ctx["item"], ctx["kind"] = item, kind
			if agent != "" {
				ctx["agent"] = agent
			}
			judge(kind, r.run, item, "timeouts."+kind, against, age, action,
				act(action, conf, ctx))
		}
		if limit, ok := conf.num("timeouts", "phase"); ok {
			for phase, age := range openPhases(r.run) {
				if age > limit && !recently("timeouts.phase", phase, limit) {
					action := conf.str("actions", "on_phase_timeout", "warn")
					ctx["phase"] = phase
					judge("phase", r.run, phase, "timeouts.phase", limit, age, action,
						act(action, conf, ctx))
				}
			}
		}
		if limit, ok := conf.num("timeouts", "session"); ok {
			for session, age := range openSpans(r.run, "session_start", "session_end", "session") {
				// Judged by silence, not by age. An implement session did two
				// and a half hours of honest engineering - a thousand model
				// round-trips, tool calls every couple of minutes - and this
				// judgement, then keyed to age, shot at it three times; killing
				// a working session restarts a thousand requests from zero. A
				// session that is TALKING is alive however old it is; a session
				// that has gone silent for the limit is gone, however young.
				quiet := sessionQuiet(r.run, session)
				if quiet < 0 {
					quiet = age
				}
				if quiet > limit && !recently("timeouts.session", session, limit) {
					action := conf.str("actions", "on_session_timeout", "warn")
					// A session is keyed agent@branch by the client. The whole
					// key goes out as {agent}: the worker's control handle
					// accepts it and ends exactly this branch's session, where
					// the bare agent name would end every branch running an
					// agent by that name. {branch} still carries the tail for
					// commands that address a branch.
					ctx["session"] = session
					ctx["agent"], ctx["branch"] = session, ""
					if at := strings.Index(session, "@"); at >= 0 {
						ctx["branch"] = session[at+1:]
					}
					judge("session", r.run, session, "timeouts.session", limit, quiet,
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
