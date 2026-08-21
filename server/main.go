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
-- Spans are looked up by what is still open in one run, which is a question
-- about (run, kind, session) and nothing else.
CREATE INDEX IF NOT EXISTS events_span ON events (run, kind, session);
CREATE INDEX IF NOT EXISTS events_phase ON events (run, kind, phase);

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
		// REPLACE, not IGNORE: if an earlier event created the row, this is
		// where it learns the ticket's name and its real start.
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
	var agent string
	if err := db.QueryRow(`SELECT at, coalesce(agent,'') FROM events
	                       WHERE run=? AND kind='request_start' AND session=?
	                       ORDER BY id DESC LIMIT 1`, run, request).
		Scan(&started, &agent); err != nil {
		return false
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

// openSpans is what this run has begun and not yet finished, by name.
//
// Asked of the database rather than replayed in here. Replaying meant reading
// every request this run had ever made — fifteen thousand rows, every tick, to
// find the two that were still open — and doing it again for phases and again
// for sessions. The answer was always "the ones with no end", which is a
// sentence SQL can say by itself.
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
		if limit, ok := conf.num("limits", "usd_per_run"); ok && r.usd > limit {
			action := conf.str("actions", "on_usd", "stop_run")
			judge("run", r.run, r.name, "limits.usd_per_run", limit, r.usd, action,
				act(action, conf, ctx))
			finish(r.run, "BLOCKED", "ammit: over limits.usd_per_run")
			continue
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
				who, _, _ := spanFacts(r.run, request)
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
			for phase, age := range openSpans(r.run, "phase_start", "phase_end", "phase") {
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
				if age > limit && !recently("timeouts.session", session, limit) {
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
	docsDir = env("AMMIT_DOCS", strings.TrimSuffix(dbPath, "/"+lastSegment(dbPath))+"/documents")
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

	serveUI(mux, confPath, env("AMMIT_CHARTS_URL", "http://localhost:3301"))

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
