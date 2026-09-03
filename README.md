<div align="center">

# ammit

Weighs a running pipeline against its limits. Eats what fails.

[![License](https://img.shields.io/badge/MIT-1a1a1a?style=flat-square&labelColor=1a1a1a)](LICENSE)
[![Python](https://img.shields.io/badge/python-3.10%2B-1a1a1a?style=flat-square&labelColor=1a1a1a)](https://www.python.org)
[![Dependencies](https://img.shields.io/badge/dependencies-none-1a1a1a?style=flat-square&labelColor=1a1a1a)](pyproject.toml)

</div>

---

A four-hour budget was enforced by a timer inside the pipeline it was meant to
restrain. The pipeline blocked, the timer never got a turn, and the run was
cancelled at ten and a half hours having cost $345. The session cap lived in the
same loop and cut sessions in half while they waited for tests, so it was
removed. A single turn hung for two hours — no error, no retry — twice in one
run.

All three are the same mistake: the thing being limited was in charge of the
limit.

Ammit sat beside the scales. A heart was weighed against the feather of truth,
Thoth wrote the result down, and what failed the weighing was eaten. That is the
whole design — keep the record, hold the measure, devour what goes past it, and
from outside, where a wedged run cannot delay its own judgement.

## The client

```python
import ammit

run = ammit.Run("APF-1934", tags={"mode": "test"})
with run.phase("implementing"):
    with run.session("implement", branch="req-3", model="sonnet") as s:
        s.turn()                                   # heartbeat: still thinking
        s.spend(usd=0.42, tokens_in=120_000, tokens_out=3_400)
        s.log("wrote the step, running the scenario")
run.finish("PASS", "31 scenarios, 0 failures")
```

No dependencies. Every send is best-effort and off the caller's thread, so a
server that is down costs the pipeline milliseconds. Point it somewhere with
`AMMIT_URL`; silence it entirely with `AMMIT_DISABLE=1`.

## What a pipeline must report

Every unit of work says when it starts, that it is still going, and how it ended.
All three, or the unit is invisible — and an invisible unit is one this service
will eventually mistake for a dead one.

| | at the start | while it runs | at the end |
|---|---|---|---|
| run | `run_start` | — | `run_end` |
| phase | `phase_start` | — | `phase_end` |
| agent session | `session_start` | `turn`, `log` | `session_end`, `spend` |
| one wait for a model | `request_start` | — | `request_end` |
| test / test module | `item_start` | — | `item_end` |
| **anything that runs for minutes without talking** | | **a line a minute** | |

That last row is the one that gets skipped, and it is the one that costs. A phase
that is a single shell command — bring the environment up, build a venv, run a
suite — sends nothing between its start and its end. From out here that is
indistinguishable from a process that died, because it is the same thing: no
events.

It happened. A phase brought an environment up for ten minutes in silence, this
service concluded the run was dead, restarted its worker fourteen times over four
hours, and each restart killed a run the previous restart had already killed.
Nothing in the record could say whether the run was working or wedged, because
the phase had never said either.

So: **a long-running step reports a line a minute.** Not for the log — for the
difference between working and wedged, which nothing else can tell.

### Every phase, not most of them

A pipeline is only as visible as its quietest step. The phases full of model
calls report themselves for free — thousands of events. The ones that are a
single command report nothing at all, and those are the slow ones: the build, the
deploy, the migration, the suite.

Two examples. A step that talks to a model:

```python
with run.phase("implementing"):
    with run.session("coder", branch="req-3", model="sonnet") as s:
        s.turn()
        s.spend(usd=0.42, tokens_in=120_000, tokens_out=3_400)
```

And a step that is a command — the one that usually gets left out:

```python
import subprocess, time

with run.phase("deploy"):
    proc = subprocess.Popen(["./deploy.sh"], stdout=subprocess.PIPE, text=True)
    said = 0.0
    for line in proc.stdout:            # line by line, not captured in a lump:
        if time.time() - said > 60:     # output that arrives at the end says
            said = time.time()          # nothing while it matters
            run.note(line.strip())
    proc.wait()
```

In a language without an SDK, the same thing is one line of shell:

```bash
curl -sS -m 3 -X POST "$AMMIT_URL/events" -H 'Content-Type: application/json' \
  -d "{\"kind\":\"log\",\"run\":\"$RUN\",\"phase\":\"deploy\",\"text\":\"$line\"}" &
```

Fire it and move on — reporting must never be why a deploy is slower, and a
dropped line costs nothing. A missing minute of them costs a run.

## Clients

One call in the pipeline's language, and none of them can slow it down: every
send is fire-and-forget.

Every language the model vendors ship an SDK for, so the report goes in the same
language as the work:

| language | SDK |
|---|---|
| Python | `pip install ammit` — `ammit.Run(...)` |
| TypeScript / JavaScript | `clients/typescript/ammit.ts` — `new Run(...)` |
| Go | `clients/go` — `ammit.NewRun(...)` |
| Java | `clients/java/Ammit.java` — `Ammit.run(...)` |
| Kotlin | `clients/kotlin/Ammit.kt` — `Ammit.run(...)`, `use {}` closes the span |
| C# / .NET | `clients/csharp/Ammit.cs` — `new Run(...)`, `using` closes the span |
| Ruby | `clients/ruby/ammit.rb` — `Ammit::Run.new(...)`, blocks close the span |
| PHP | `clients/php/Ammit.php` — `new Run(...)` |
| anything else | `clients/shell/ammit.sh` — `ammit_turn`, `ammit_spend`, `ammit_run_finish` |

None of them has a dependency, and none of them can slow the caller: every send
is fire-and-forget and every failure is swallowed.

The shell one is not a joke. The steps inside a pipeline that are shell scripts
hang exactly as often as the ones that are not, and until something reports them
they are the invisible half.

## The server

```bash
docker run -p 8099:8099 \
  -v "$PWD/limits.yml:/config/limits.yml:ro" \
  -v ammit-data:/data \
  -v /var/run/docker.sock:/var/run/docker.sock \
  ghcr.io/ngavrish/ammit
```

One Go binary, one sqlite file. Keeps every event, weighs on a tick, acts by
running a command.

## The scales

```yaml
queue:
  parallel: 1              # runs allowed at once

timeouts:
  run: 14400               # the whole thing
  phase: 5400              # one phase
  session: 3600            # one agent session
  turn: 600                # one exchange — the hang nothing inside the run catches

limits:
  usd_per_run: 150
  turns_per_run: 4000

actions:
  on_run_timeout: stop_run
  on_usd: stop_run
  on_turn_timeout: restart_worker
  on_start: start_run

commands:
  restart_worker: docker restart {worker}
  stop_run: docker exec {worker} sh -c "rm -f /runs/{name}/.running"
  start_run: curl -sS -X POST {starter} -d {payload}

context:
  worker: my-pipeline-worker-1
  starter: http://orchestrator:8081/api/run
```

Actions are commands, not code: it never needs to know whether it is watching a
container, a pod or a process. The file is re-read every tick, so a limit can be
changed while a run is going. Zero means no limit, and writing zero is a decision
rather than an omission.

## Twice a limit

Crossing a limit is ordinary: an estimate was low, a phase was unlucky, and a
warning is the right size of answer. Crossing it twice over is a different claim
— that whatever the limit encoded is no longer true of this run — and it wants
reading rather than noting.

```yaml
escalate:
  factor: 2
actions:
  on_turns: warn                    # 1600 turns is a note
  on_turns_per_run_over: stop_run   # 3200 is a run to go and read
```

Any rule may name its own second action as `on_<rule>_over`; without one,
`actions.on_escalation` applies, and without that the ordinary action stands.

## Gates and repair

A pipeline that checks its own work has two questions nobody usually answers:
how often each check refuses, and how long the repair then takes. Between them
they separate a gate that earns its cost from a tollbooth.

```python
run.gate("planrules", verdict="red", findings=18, seconds=3)
```

The round is counted here rather than taken from the caller: the pipeline knows
what it found, this service knows how many times it has been told. What counts
as a finding stays the pipeline's business — a watchdog that parses somebody
else's log is a watchdog that breaks when the log is reworded.

Charted: findings per gate round by round, what a round costs, and a table of
rounds-to-green per gate. One round is a check doing its job; three is a repair
that cannot hear what is being asked.

## The queue

```bash
curl -X POST localhost:8099/queue -d '{"name": "APF-2531", "payload": {"mode": "test"}}'
```

Items start when a slot frees, in order, by running the configured command. "Two
at once" becomes a number in a file rather than a property of whoever pressed the
button first.

## What it exposes

| endpoint | what |
|---|---|
| `POST /events` | what the client reports |
| `POST /queue` | queue an item |
| `GET /runs` | runs with cost, turns, verdicts |
| `GET /gates` | what each check decided, and how many rounds it took |
| `GET /judgements` | every limit crossed, what was observed, what was done |
| `GET /queue` | what is waiting |
| `GET /limits` | the config as the server currently reads it |
| `GET /` | the page: limits, queue, runs, judgements |
| `GET /limits.yml` | the config file itself |
| `PUT /limits.yml` | replace it — refused if it does not parse |

## The page

`http://localhost:8099` is the limits as fields — each timeout, budget and queue
size with its own box, what it means beside it, and seconds shown as hours while
you type. Change one, save, and it is in force on the next tick: no restart, and
nothing lost mid-run.

Underneath it is the same file, edited in place: the comments, the order and the
alignment somebody wrote survive being changed by somebody else in a hurry, so
the config still lives in git and every change is a diff one line long. What
fields cannot say — a new command, a renamed section — is one click away as the
file itself.

The page also queues a ticket, and lists the runs, the queue and every judgement,
which is the short version of the charts for when the charts are one tab too many.

## The dashboard

`deploy/` ships a Grafana that reads the same sqlite file the server judges by,
so what is drawn and what was acted on cannot disagree.

**Every limit is on the chart it applies to**, the way a level is on a trading
screen: a dashed red line held flat until somebody moves it. It is not a number
typed into a panel — the server writes the limits down on every tick, so the line
is what the run was actually being measured against, and editing a limit mid-run
bends the line at the minute it was edited, with a marker on the axis saying who
moved what.

Six rows, and every one of them asks the same four questions — money, turns,
time, waiting — of a different thing:

| row | what it answers |
|---|---|
| the run | cost, turns, age and idle time piling up, per run |
| requests to the model | how long one request took, which ones died, by agent and by phase |
| by phase | cost, turns, length, idle and memory, for each phase of the run |
| by agent session | the same five, for each agent |
| the machine | memory, cpu and process count per container |
| where the time went | busy against idle per run, and the longest gaps in full |

with the limit drawn on each: `limits.usd_per_run`, `limits.turns_per_run`,
`timeouts.run`, `timeouts.phase`, `timeouts.session`, `timeouts.request`,
`timeouts.turn`, `limits.memory_mb`, `queue.parallel`.

Three of these exist because nothing else could see them.

**What a prompt carries** is the newest, and the one that cost the most. A run
that spends too much says so in dollars; a run whose prompt is twice the size it
needs to be says nothing at all and looks like ordinary work. One run carried a
253 KB document in every message — 27 million tokens re-sent, a quarter of that
run, invisible in both the money and the turns. `limits.turn_tokens` is what one
turn was sent, weighed while the next turn has not been paid for yet.
 **A request to the model
that never comes back** has no error to log and no retry to fire — from inside it
is indistinguishable from a long think, and one sat for two hours twice in a
single run. It is timed from out here, and `timeouts.request` is a thing that can
be acted on. **Memory** is read from outside for the same reason: a worker the
kernel kills leaves no evidence, because the process that would have said so is
the one that died. Every judgement is an annotation
on the same axis, so "the cost stopped climbing" and "the run was stopped" are one
event rather than two stories.

Dashboards are provisioned from files and editable in the interface — add a panel,
keep it; the file is the starting point, not the ceiling. The committed JSON is
kept in `server/*.json` - one file per page, embedded in the binary - because twenty-nine panels of
near-identical JSON is not a thing to edit by hand.

```bash
cd deploy && docker compose up -d      # ammit on :8099, Grafana on :3301
```

## Why not Temporal, Prefect, Airflow

They are better at what they do, and adopting one means rewriting the pipeline as
their workflow. Ammit assumes the pipeline exists and adds one client call: the
limits, the queue and the dashboard arrive without a rewrite. If the control
plane is the thing you want to replace, use Temporal.

## License

MIT
