package main

import (
	"database/sql"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// What is weighed, what is done about it, and how it is written down.

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

// When this process started. An age measured across our own absence is
// evidence about us, not about the run.
//
// The sleep guard in main.go covers a tick that arrives late; it cannot cover
// a restart, because a restart resets that clock to now. Run e3d2c550 died to
// exactly that: this service hung on one expensive query for nineteen minutes,
// took no heartbeats while it was down, and on coming back read a pulse aged
// 1163s against a limit of 120 - so its first act was to fire restart_worker
// at a run that had been alive and fine the whole time, breaking the stream
// and ending it.
//
// Nothing older than our own uptime is ours to judge.
var _upSince = time.Now()

func weigh(conf Config) {
	up := time.Since(_upSince).Seconds()
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
			if pulse := heartbeatAge(r.run); pulse >= 0 && pulse > hb && pulse < up {
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
		// The wait the model never saw: the CLI retrying its own requests
		// with back-off, on 429 and 529, and saying so in api_retry system
		// messages. Run 16922dd7 spent most of a twenty-minute fan-out there
		// and was read as a slow model, then as an API ceiling; a probe put
		// the ceiling at twenty times what the run got. Seconds per session
		// inside limits.spin_window; the answer is warn, because a throttled
		// account is a fact to record and nobody to kill.
		if limit, ok := conf.num("limits", "retry_wait"); ok && limit > 0 {
			window, _ := conf.num("limits", "spin_window")
			if window <= 0 {
				window = 600
			}
			for session, secs := range retryWaits(r.run, window) {
				if secs <= limit || recently("limits.retry_wait", r.run+session, window) {
					continue
				}
				action := conf.str("actions", "on_retry_wait", "warn")
				ctx["session"] = session
				ctx["agent"], ctx["branch"] = session, ""
				if at := strings.Index(session, "@"); at >= 0 {
					ctx["branch"] = session[at+1:]
				}
				judge("session", r.run, session, "limits.retry_wait", limit, secs,
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
		// One number for every phase is one number for phases that are not
		// alike. Planning ran 4 to 20 minutes across six runs and then took
		// 37 on the seventh, well inside a 90-minute ceiling that was set for
		// the implement phase and says nothing about this one. A limit that
		// no realistic failure can reach is not a limit.
		//
		// So `timeouts.phase_<name>` overrides `timeouts.phase` for that
		// phase, and the judgement says which one it was measured against.
		if base, ok := conf.num("timeouts", "phase"); ok {
			for phase, age := range openPhases(r.run) {
				limit, named := base, "timeouts.phase"
				if own, has := conf.num("timeouts", "phase_"+phase); has && own > 0 {
					limit, named = own, "timeouts.phase_"+phase
				}
				if age > limit && !recently(named, phase, limit) {
					// actions.on_phase_timeout_<name> overrides the general
					// action: a phase whose overrun means the run is lost
					// (funcreqs at twenty minutes) ends the run, not the phase.
					action := conf.str("actions", "on_phase_timeout_"+phase,
						conf.str("actions", "on_phase_timeout", "warn"))
					ctx["phase"] = phase
					judge("phase", r.run, phase, named, limit, age, action,
						act(action, conf, ctx))
				}
			}
		}
		// The silence limit below cannot see a session that never stops
		// talking: a funcreq branch made fifty turns in twenty minutes on
		// run 16922dd7 and was quiet for no more than four minutes at a
		// stretch. `timeouts.session_age_<agent>` is measured against the
		// session's AGE, from session_start, for that agent's sessions only;
		// actions.on_session_age_<agent> answers it, stop_run by default,
		// because a session that old has lost the run, not the branch.
		for session, age := range openSpans(r.run, "session_start", "session_end", "session") {
			agent := session
			if at := strings.Index(session, "@"); at >= 0 {
				agent = session[:at]
			}
			limit, has := conf.num("timeouts", "session_age_"+agent)
			if !has || limit <= 0 || age <= limit || recently("timeouts.session_age_"+agent, session, limit) {
				continue
			}
			action := conf.str("actions", "on_session_age_"+agent, "stop_run")
			ctx["session"] = session
			ctx["agent"], ctx["branch"] = session, ""
			if at := strings.Index(session, "@"); at >= 0 {
				ctx["branch"] = session[at+1:]
			}
			judge("session", r.run, session, "timeouts.session_age_"+agent, limit, age,
				action, act(action, conf, ctx))
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
