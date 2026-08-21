// Package ammit reports what a long-running pipeline is doing, so something
// outside it can judge.
//
// Every send is fire-and-forget on its own goroutine: a watchdog that can slow
// down or stop the pipeline it watches is not a watchdog. Standard library only.
//
//	run := ammit.NewRun("APF-1934", nil)
//	phase := run.Phase("implementing")
//	s := run.Session("implement", "req-3", "sonnet")
//	s.Turn("")
//	s.Spend(0.42, 120000, 3400)
//	s.End(nil)
//	phase.End(nil)
//	run.Finish("PASS", "31 scenarios, 0 failures")
package ammit

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"time"
)

var (
	endpoint = envOr("AMMIT_URL", "http://ammit:8099")
	enabled  = os.Getenv("AMMIT_DISABLE") != "1"
	client   = &http.Client{Timeout: 3 * time.Second}
	quiet    sync.Once
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Endpoint points the client somewhere other than AMMIT_URL.
func Endpoint(url string) { endpoint = url }

// Send reports one fact. It never blocks and never returns an error: reporting
// must not be the reason a run is slower or stops.
func Send(kind string, fields map[string]any) {
	if !enabled {
		return
	}
	payload := map[string]any{"kind": kind, "at": float64(time.Now().UnixNano()) / 1e9}
	for k, v := range fields {
		payload[k] = v
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	go func() {
		resp, err := client.Post(endpoint+"/events", "application/json", bytes.NewReader(body))
		if err != nil {
			quiet.Do(func() { println("ammit: not reporting:", err.Error()) })
			return
		}
		resp.Body.Close()
	}()
}

func encode(kind string, fields map[string]any) []byte {
	payload := map[string]any{"kind": kind,
		"at": float64(time.Now().UnixNano()) / 1e9}
	for k, v := range fields {
		payload[k] = v
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return body
}

// SendAndWait is Send for the events whose loss leaves a shape behind: the ones
// that close a span. Everything else is fire-and-forget on purpose.
func SendAndWait(kind string, fields map[string]any) {
	if !enabled {
		return
	}
	body := encode(kind, fields)
	if body == nil {
		return
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Post(endpoint+"/events", "application/json",
		bytes.NewReader(body))
	if err == nil {
		resp.Body.Close()
	}
}

func id() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Run is one unit of work the outside world cares about — a ticket, a job.
type Run struct {
	ID    string
	Name  string
	phase string
	t0    time.Time
}

func NewRun(name string, tags map[string]string) *Run {
	r := &Run{ID: id(), Name: name, t0: time.Now()}
	Send("run_start", map[string]any{"run": r.ID, "name": name, "tags": tags})
	return r
}

func (r *Run) Finish(verdict, summary string) {
	Send("run_end", map[string]any{"run": r.ID, "verdict": verdict, "summary": summary,
		"seconds": time.Since(r.t0).Seconds()})
}

// Adopt joins a run somebody else opened, instead of starting another one.
//
// The work does not always begin where the reporting does. One service kicks a
// ticket off and deploys an environment for minutes before a second service is
// even dispatched to do the thinking; if each mints its own id, one piece of
// work becomes two runs, neither of which is the whole thing, and a limit on
// "the run" applies to half of it. Whoever starts the work opens the id and
// hands it over — through a shared file, a queue message, an environment
// variable, whatever the two already have between them.
func Adopt(id, name string) *Run {
	return &Run{ID: id, Name: name, t0: time.Now()}
}

// StepSpan is one unit of work that is not a conversation with a model: a
// deploy, a build, a migration, a test module.
//
// It exists because these are the steps that go quiet. A phase full of model
// calls reports itself for free — thousands of events. A phase that is one
// command sends nothing between its start and its end, and from outside that is
// indistinguishable from a process that died, because it is the same thing: no
// events. Report the start, a line a minute while it runs, and the end.
type StepSpan struct {
	run     *Run
	name    string
	kind    string
	started time.Time
}

// Step opens a unit of work. `kind` is what sort it is — "deploy", "module",
// "gate" — and the server looks up timeouts.<kind> under that name, so a
// pipeline can bound its own units without this library knowing what they are.
func (r *Run) Step(name, kind string) *StepSpan {
	s := &StepSpan{run: r, name: name, kind: kind, started: time.Now()}
	Send("item_start", map[string]any{"run": r.ID, "item": name, "session": name,
		"itemkind": kind, "phase": name})
	return s
}

// Log says the step is still going, and what it is doing. Once a minute is
// enough: it is not for the log, it is for the difference between working and
// wedged, which nothing else can tell.
func (s *StepSpan) Log(text string) {
	Send("log", map[string]any{"run": s.run.ID, "item": s.name, "session": s.name,
		"phase": s.name, "level": "tool", "text": text})
}

// End closes the step with how long it took and whether it worked.
//
// Sent and waited for, unlike everything else here. A step that opens and never
// closes reads, to anything watching, as a step still running — for ever — and a
// short-lived process that fires this into a goroutine and then exits kills the
// goroutine before it sends anything.
func (s *StepSpan) End(err error) {
	fields := map[string]any{"run": s.run.ID, "item": s.name, "session": s.name,
		"itemkind": s.kind, "phase": s.name, "ok": err == nil,
		"seconds": time.Since(s.started).Seconds()}
	if err != nil {
		fields["error"] = err.Error()
	}
	SendAndWait("item_end", fields)
}

func (r *Run) Note(text string) {
	Send("note", map[string]any{"run": r.ID, "text": text})
}

// PhaseSpan is a named stretch of a run.
type PhaseSpan struct {
	run  *Run
	name string
	t0   time.Time
}

func (r *Run) Phase(name string) *PhaseSpan {
	r.phase = name
	Send("phase_start", map[string]any{"run": r.ID, "phase": name})
	return &PhaseSpan{run: r, name: name, t0: time.Now()}
}

func (p *PhaseSpan) End(err error) {
	Send("phase_end", map[string]any{"run": p.run.ID, "phase": p.name,
		"seconds": time.Since(p.t0).Seconds(), "failed": err != nil})
	p.run.phase = ""
}

// SessionSpan is one conversation with a model, or one long tool call.
//
// Turn is the heartbeat the server watches: a session that stops calling it is
// waiting on something that is not coming back.
type SessionSpan struct {
	run               *Run
	ID, Agent, Branch string
	Model             string
	turns             int
	usd               float64
	t0                time.Time
}

func (r *Run) Session(agent, branch, model string) *SessionSpan {
	s := &SessionSpan{run: r, ID: id(), Agent: agent, Branch: branch, Model: model,
		t0: time.Now()}
	Send("session_start", map[string]any{"run": r.ID, "session": s.ID, "agent": agent,
		"branch": branch, "model": model, "phase": r.phase})
	return s
}

func (s *SessionSpan) Turn(note string) {
	s.turns++
	Send("turn", map[string]any{"run": s.run.ID, "session": s.ID, "agent": s.Agent,
		"branch": s.Branch, "phase": s.run.phase, "n": s.turns, "note": note})
}

func (s *SessionSpan) Spend(usd float64, tokensIn, tokensOut int) {
	s.usd += usd
	Send("spend", map[string]any{"run": s.run.ID, "session": s.ID, "agent": s.Agent,
		"phase": s.run.phase, "usd": usd, "tokens_in": tokensIn, "tokens_out": tokensOut})
}

func (s *SessionSpan) Log(text string) {
	Send("log", map[string]any{"run": s.run.ID, "session": s.ID, "agent": s.Agent,
		"branch": s.Branch, "phase": s.run.phase, "text": text})
}

func (s *SessionSpan) End(err error) {
	fields := map[string]any{"run": s.run.ID, "session": s.ID, "agent": s.Agent,
		"seconds": time.Since(s.t0).Seconds(), "turns": s.turns, "usd": s.usd,
		"failed": err != nil}
	if err != nil {
		fields["error"] = err.Error()
	}
	Send("session_end", fields)
}
