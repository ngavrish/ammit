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

| chart | against |
|---|---|
| cost per run, as it accrued | `limits.usd_per_run` |
| turns per run | `limits.turns_per_run` |
| how long a run has been going | `timeouts.run` |
| silence between turns, per agent | `timeouts.turn` |
| session length | `timeouts.session` |
| phase length | `timeouts.phase` |
| runs at once | `queue.parallel` |

The silence chart is the one this was written for: a model call that never came
back is a spike there and nothing anywhere else. Every judgement is an annotation
on the same axis, so "the cost stopped climbing" and "the run was stopped" are one
event rather than two stories.

Dashboards are provisioned from files and editable in the interface — add a panel,
keep it; the file is the starting point, not the ceiling.

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
