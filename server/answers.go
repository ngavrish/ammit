package main

import (
	"context"
	"database/sql"
	"log"
	"strings"
	"time"
)

// How long the model takes to answer.
//
// A request_end is one message off the SDK's iterator, not one model turn:
// a turn that thinks for forty seconds arrives as thirty thinking_tokens
// SystemMessages and then the AssistantMessage, each its own "request", and
// `session` is the request again. Averaging requests gave ~1s "turns" that
// were really slices of one; summing by request id added up unrelated
// sessions, because a request id restarts at #1 in every session and every
// fan-out branch.
//
// So the unit is an answer, per stream (run, branch, agent, phase), in the
// order the messages came:
//
//   - a wait that is not for the model (a tool ran) starts the clock again;
//   - a model wait that is really the agent idling - its own background
//     command finishing (Task*Message), a rate-limit notice, the result
//     message - is not the model thinking: it is kept aside, per phase, as
//     background or retry time, and not added to any answer;
//   - every other model wait adds its seconds, api_retry back-offs included
//     (they are also counted as the answer's retry_s);
//   - an AssistantMessage is the answer: the seconds so far are one row of
//     model_answers, and the clock starts again.
//
// The model is the turn events' for the same (run, branch, agent) - the
// largest name, as the reference query took it. It is often not known when
// the answer is made (a turn event is flushed when the next turn opens), so
// an answer may sit with no model until the first turn of its stream lands.
//
// Rows are made on ingest, never on demand; the history is replayed through
// the same code once, in slices of time, with the lock let go between slices.

var answerIdle = map[string]string{
	"TaskNotificationMessage": "background",
	"TaskStartedMessage":      "background",
	"TaskUpdatedMessage":      "background",
	"RateLimitEvent":          "retry",
	"ResultMessage":           "",
}

type streamKey struct{ run, branch, agent, phase string }
type agentKey struct{ run, branch, agent string }
type answerAcc struct{ seconds, retry, at float64 }
type bucket struct {
	day          int64
	phase, model string
}

var (
	answerStreams = map[streamKey]*answerAcc{}
	streamModels  = map[agentKey]string{}
	// False until the replay has caught up; until then store() leaves these
	// events to the replay, which reads them from events itself.
	answersLive bool
)

type execer interface {
	Exec(string, ...any) (sql.Result, error)
	Query(string, ...any) (*sql.Rows, error)
}

var bg = context.Background()

func closeRows(rs *sql.Rows) {
	if err := rs.Err(); err != nil {
		log.Printf("ammit: answers: %v", err)
	}
	if err := rs.Close(); err != nil {
		log.Printf("ammit: answers: %v", err)
	}
}

type answerBatch struct {
	x       execer
	touched map[streamKey]bool
	dirty   map[bucket]bool
	last    float64
}

func newAnswerBatch(x execer) *answerBatch {
	return &answerBatch{x: x, touched: map[streamKey]bool{}, dirty: map[bucket]bool{}}
}

func dayOf(at float64) int64 { return int64(at) / 86400 }

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (b *answerBatch) exec(q string, args ...any) {
	if _, err := b.x.Exec(q, args...); err != nil {
		log.Printf("ammit: answers: %v", err)
	}
}

// add is one event: a turn names the model of its stream, a request_end moves
// the clock.
func (b *answerBatch) add(id int64, at float64, kind, run, branch, phase, agent, wait, msg string,
	seconds float64, retry bool, model string) {
	if at > b.last {
		b.last = at
	}
	ak := agentKey{run, branch, agent}
	if kind == "turn" {
		if model == "" || model <= streamModels[ak] {
			return
		}
		streamModels[ak] = model
		b.exec(`INSERT INTO model_of (run, branch, agent, model) VALUES (?,?,?,?)
		        ON CONFLICT(run, branch, agent) DO UPDATE SET model=excluded.model`,
			run, branch, agent, model)
		// Every answer of this stream so far moves to the model, and the
		// buckets it leaves and joins are due again.
		for _, t := range []string{"model_answers", "model_idle"} {
			if rs, err := b.x.Query(`SELECT DISTINCT day, phase, coalesce(model,'') FROM `+t+`
			        WHERE run=? AND branch=? AND agent=? AND model IS NOT ?`, run, branch, agent, model); err == nil {
				for rs.Next() {
					var k bucket
					if rs.Scan(&k.day, &k.phase, &k.model) == nil {
						if k.model != "" {
							b.dirty[k] = true
						}
						k.model = model
						b.dirty[k] = true
					}
				}
				closeRows(rs)
			}
			b.exec(`UPDATE `+t+` SET model=? WHERE run=? AND branch=? AND agent=? AND model IS NOT ?`,
				model, run, branch, agent, model)
		}
		return
	}
	if kind != "request_end" {
		return
	}
	sk := streamKey{run, branch, agent, phase}
	st := answerStreams[sk]
	if st == nil {
		st = &answerAcc{}
		answerStreams[sk] = st
	}
	b.touched[sk] = true
	st.at = at
	m := streamModels[ak]
	if wait != "model" {
		st.seconds, st.retry = 0, 0
		return
	}
	if idle, ok := answerIdle[msg]; ok {
		if idle == "" || seconds == 0 {
			return
		}
		b.exec(`INSERT OR IGNORE INTO model_idle (event_id, at, day, run, branch, agent, phase, model, kind, seconds)
		        VALUES (?,?,?,?,?,?,?,?,?,?)`,
			id, at, dayOf(at), run, branch, agent, phase, nullable(m), idle, seconds)
		if m != "" {
			b.dirty[bucket{dayOf(at), phase, m}] = true
		}
		return
	}
	st.seconds += seconds
	if retry {
		st.retry += seconds
	}
	if msg != "AssistantMessage" {
		return
	}
	b.exec(`INSERT OR IGNORE INTO model_answers (event_id, at, day, run, branch, agent, phase, model, seconds, retry_s)
	        VALUES (?,?,?,?,?,?,?,?,?,?)`,
		id, at, dayOf(at), run, branch, agent, phase, nullable(m), st.seconds, st.retry)
	if m != "" {
		b.dirty[bucket{dayOf(at), phase, m}] = true
	}
	st.seconds, st.retry = 0, 0
}

// flush writes the clocks that moved, the day buckets that changed, and how
// far the replay has read.
func (b *answerBatch) flush() {
	for sk := range b.touched {
		st := answerStreams[sk]
		b.exec(`INSERT OR REPLACE INTO model_streams (run, branch, agent, phase, seconds, retry_s, at)
		        VALUES (?,?,?,?,?,?,?)`, sk.run, sk.branch, sk.agent, sk.phase, st.seconds, st.retry, st.at)
	}
	for k := range b.dirty {
		b.exec(`INSERT OR REPLACE INTO phase_model_times
		          (day, phase, model, answers, min_s, sum_s, max_s, over_120, retry_s, background_s)
		        SELECT ?1, ?2, ?3, count(*), min(seconds), coalesce(sum(seconds),0), max(seconds),
		               coalesce(sum(seconds>120),0),
		               coalesce(sum(retry_s),0) + (SELECT coalesce(sum(seconds),0) FROM model_idle
		                    WHERE day=?1 AND phase=?2 AND model=?3 AND kind='retry'),
		               (SELECT coalesce(sum(seconds),0) FROM model_idle
		                    WHERE day=?1 AND phase=?2 AND model=?3 AND kind='background')
		        FROM model_answers WHERE day=?1 AND phase=?2 AND model=?3`, k.day, k.phase, k.model)
	}
	if b.last > 0 {
		b.exec(`INSERT INTO lift_marks (name, at) VALUES ('answers', ?)
		        ON CONFLICT(name) DO UPDATE SET at=max(at, excluded.at)`, b.last)
	}
	b.touched, b.dirty = map[streamKey]bool{}, map[bucket]bool{}
}

// liftAnswer is the live path, called from lift under mu.
func liftAnswer(id int64, at float64, e event) {
	if !answersLive {
		return
	}
	kind := e.s("kind")
	if kind != "turn" && kind != "request_end" {
		return
	}
	b := newAnswerBatch(db)
	b.add(id, at, kind, e.s("run"), e.s("branch"), e.s("phase"), e.s("agent"), e.s("wait"),
		e.s("msg"), e.f("seconds"), strings.Contains(e.s("detail"), "api_retry"), e.s("model"))
	b.flush()
}

// backfillAnswers replays the events the tables have not seen, six hours of
// them at a time, then turns the live path on in the same locked moment as
// the last slice, so nothing lands between the replay and the live path.
func backfillAnswers() {
	started := time.Now()
	mu.Lock()
	var mark float64
	err := db.QueryRowContext(bg, `SELECT coalesce((SELECT at FROM lift_marks WHERE name='answers'),
	                              (SELECT min(at)-1 FROM events WHERE kind IN ('request_end','turn')), 0)`).Scan(&mark)
	if err != nil {
		log.Printf("ammit: answers backfill: %v", err)
	}
	loadAnswerState()
	mu.Unlock()
	const slice = 6 * 3600.0
	n := 0
	for {
		mu.Lock()
		now := float64(time.Now().Unix())
		to := mark + slice
		last := to >= now
		if last {
			to = 1e12
		}
		tx, err := db.BeginTx(bg, nil)
		if err != nil {
			mu.Unlock()
			log.Printf("ammit: answers backfill: %v", err)
			return
		}
		b := newAnswerBatch(tx)
		rs, err := tx.QueryContext(bg, `SELECT id, at, kind, coalesce(run,''), coalesce(branch,''), coalesce(phase,''),
		        coalesce(agent,''), coalesce(json_extract(payload,'$.wait'),''),
		        coalesce(json_extract(payload,'$.msg'),''), coalesce(json_extract(payload,'$.seconds'),0),
		        instr(coalesce(json_extract(payload,'$.detail'),''),'api_retry')>0,
		        coalesce(json_extract(payload,'$.model'),'')
		        FROM events WHERE kind IN ('request_end','turn') AND at > ? AND at <= ? ORDER BY at, id`, mark, to)
		if err != nil {
			_ = tx.Rollback()
			mu.Unlock()
			log.Printf("ammit: answers backfill: %v", err)
			return
		}
		type ev struct {
			id                                            int64
			at, s                                         float64
			kind, run, branch, phase, agent, wait, msg, m string
			retry                                         bool
		}
		var evs []ev
		for rs.Next() {
			var e ev
			if rs.Scan(&e.id, &e.at, &e.kind, &e.run, &e.branch, &e.phase, &e.agent, &e.wait, &e.msg,
				&e.s, &e.retry, &e.m) == nil {
				evs = append(evs, e)
			}
		}
		closeRows(rs)
		for _, e := range evs {
			b.add(e.id, e.at, e.kind, e.run, e.branch, e.phase, e.agent, e.wait, e.msg, e.s, e.retry, e.m)
		}
		n += len(evs)
		if b.last == 0 && !last {
			b.last = to
		}
		b.flush()
		if err := tx.Commit(); err != nil {
			log.Printf("ammit: answers backfill: %v", err)
		}
		if last {
			answersLive = true
			// A stream that stopped a day ago is not going to answer; its
			// half-counted clock is dropped rather than kept forever.
			if _, err := db.ExecContext(bg, `DELETE FROM model_streams WHERE at < ?`, now-86400); err != nil {
				log.Printf("ammit: answers: %v", err)
			}
			mu.Unlock()
			break
		}
		mu.Unlock()
		mark = to
		time.Sleep(20 * time.Millisecond)
	}
	log.Printf("ammit: answers: replayed %d events in %s; live", n, time.Since(started).Round(time.Millisecond))
}

func loadAnswerState() {
	if rs, err := db.QueryContext(bg, `SELECT run, branch, agent, phase, seconds, retry_s, at FROM model_streams`); err == nil {
		for rs.Next() {
			var k streamKey
			st := &answerAcc{}
			if rs.Scan(&k.run, &k.branch, &k.agent, &k.phase, &st.seconds, &st.retry, &st.at) == nil {
				answerStreams[k] = st
			}
		}
		closeRows(rs)
	}
	if rs, err := db.QueryContext(bg, `SELECT run, branch, agent, model FROM model_of`); err == nil {
		for rs.Next() {
			var k agentKey
			var m string
			if rs.Scan(&k.run, &k.branch, &k.agent, &m) == nil {
				streamModels[k] = m
			}
		}
		closeRows(rs)
	}
}

// answerSummary is the table the page and /stats/model-turns show, for a
// window in milliseconds: every percentile from the answers themselves, the
// background and retry time beside them. The positions are the reference
// script's: the value at n/2, int(n*.9), int(n*.99) of the sorted answers.
const answerSummary = `(WITH a AS (
    SELECT phase, model, seconds, retry_s FROM model_answers
    WHERE at*1000 >= $__from AND at*1000 <= $__to AND phase <> '' AND model IS NOT NULL),
  r AS (SELECT phase, model, seconds, retry_s,
          row_number() OVER (PARTITION BY phase, model ORDER BY seconds) - 1 AS i,
          count(*) OVER (PARTITION BY phase, model) AS n FROM a),
  g AS (SELECT phase, model, n AS answers, min(seconds) AS mn, avg(seconds) AS av, max(seconds) AS mx,
          max(CASE WHEN i = n/2 THEN seconds END) AS p50,
          max(CASE WHEN i = CAST(n*0.9 AS INTEGER) THEN seconds END) AS p90,
          max(CASE WHEN i = CAST(n*0.99 AS INTEGER) THEN seconds END) AS p99,
          100.0*sum(seconds > 120)/n AS over, sum(seconds) AS total, sum(retry_s) AS rs
        FROM r GROUP BY phase, model),
  idle AS (SELECT phase, model,
          sum(CASE WHEN kind='background' THEN seconds ELSE 0 END) AS bg,
          sum(CASE WHEN kind='retry' THEN seconds ELSE 0 END) AS rl
        FROM model_idle WHERE at*1000 >= $__from AND at*1000 <= $__to AND model IS NOT NULL
        GROUP BY phase, model)
  SELECT g.phase AS phase, g.model AS model, answers,
         round(mn,1) AS min_s, round(av,1) AS avg_s, round(p50,1) AS p50_s, round(p90,1) AS p90_s,
         round(p99,1) AS p99_s, round(mx,0) AS max_s, round(over,2) AS over_2min_pct,
         round(coalesce(rs,0) + coalesce(idle.rl,0), 0) AS retry_s,
         round(100.0*(coalesce(rs,0) + coalesce(idle.rl,0))/max(total,1), 1) AS retry_pct,
         round(coalesce(idle.bg,0), 0) AS background_s
  FROM g LEFT JOIN idle ON idle.phase = g.phase AND idle.model = g.model)`

// fillAnswers expands the $__model_answers macro a panel reads the summary by.
func fillAnswers(q string) string {
	return strings.ReplaceAll(q, "$__model_answers", answerSummary)
}
