# The record

Everything this service stores, on one page.

**If a field is not on this page it is not stored.** Not kept for later, not
tucked into the payload against the day somebody wants it. It is dropped when
the event lands, and the reply to that send names it, so a client that has been
reporting a field for a month finds out on its next call rather than on the day
somebody goes looking for the number.

That is the whole of the contract, and the reason it can be believed is that
this page is enforced rather than described: `server/record.go` holds the same
list, `POST /events` filters against it, and `GET /record` serves it as JSON
with a tally of what has been turned away since the process started.

```bash
curl -X POST localhost:8099/events -H 'Content-Type: application/json' \
  -d '{"kind":"log","run":"APF-1934","text":"a line","weight":42}'
{"dropped":["weight"],"ok":true}
```

## The envelope

Seven fields, allowed on every kind: who said it, when, and where in the run.

| field | type | what |
|---|---|---|
| `kind` | string | which of the kinds below this is. Required |
| `at` | epoch seconds, float | when it happened. Filled in on arrival if absent |
| `run` | string | the run id. Any event may create the run's row |
| `phase` | string | the phase it happened in |
| `session` | string | the agent session, or for a request the wait's own id (`sid#seq`) |
| `agent` | string | which agent |
| `branch` | string | which branch of a fan-out |

They become the columns of the same name in `events`, which is why they are
indexed and why every other field is not: the envelope is what a query narrows
by, and the rest is what it then reads.

## What each kind carries

Everything below is on top of the envelope. A kind not in this table keeps its
envelope and nothing else, because a kind nothing reads is a payload stored for
nobody.

### The run

| kind | fields |
|---|---|
| `run_start` | `name` the ticket, `tags` a free map |
| `run_end` | `verdict`, `summary`, `seconds`, `usd` what the run cost in total, `steps` how many steps the flow took |
| `flow` | `mode`, `phases` the sequence this run actually executed |

The runner closes a run with `finish(**extra)`, and `usd` and `steps` are what
its call sites pass into that: two named fields rather than a hole in the page.
A third one arrives the way every field arrives, by being added here first.

### A phase

| kind | fields |
|---|---|
| `phase_start` | nothing beyond the envelope |
| `phase_end` | `seconds`, `failed`, `ok`, `error`, `text` what the phase said, as prose, to its first 8000 characters |

`phase_end.text` is the phase's own body, which is why a phase is findable by a
word in it: `text` is one of the fields `GET /search` reads.

### An agent session

| kind | fields |
|---|---|
| `session_start` | `model`, `prefix` the byte size of each part the session opened with: the rules, the system append, each slot of the prompt |
| `session_end` | `seconds`, `turns`, `usd`, `failed`, `error`, `ok`, `stopped`, `model`, `tokens_in`, `tokens_out`, `cache_read`, `cache_write` the session's own four counts, `denied` how many times it was refused a tool |
| `turn` | `n`, `note`, `model`, `request`, `context`, `tokens_in`, `tokens_out`, `out_est`, `cache_read`, `cache_write`, `cache_write_1h`, `geo` |
| `spend` | `usd`, `tokens_in`, `tokens_out`, `cache_read`, `cache_write` |
| `log` | `level`, `text`, `seq` where this line came in the session's transcript |
| `note` | `text`, `tags` a free map |

`turn` carries two counts of one thing on purpose. `tokens_out` is exactly what
the SDK reported and `out_est` is the runner's own measure of the message. They
are not folded together here: a column named `tokens_out` that is sometimes an
estimate is a lie nothing downstream can undo, so a reader that wants output
takes the larger of the two and knows that it did.

### One wait for a model

| kind | fields |
|---|---|
| `request_start` | `wait` (`tool` or `model`), `model`, `request` the wait's own id |
| `request_end` | `seconds`, `out`, `ok`, `error`, `detail`, `wait`, `model`, `request`, `msg` the class of SDK message that ended the wait |

The request's own id travels in `session` and again in `request`, which is the
field a `turn` and a `call` carry to name the wait they happened in. One name
on three kinds is what makes a wait, its turn and its tool calls one trace.

### A test, a check, a repair

| kind | fields |
|---|---|
| `item_start` | `item`, `itemkind` |
| `item_end` | `item`, `itemkind`, `ok`, `seconds`, `failed`, `error` |
| `gate` | `verdict`, `findings`, `seconds` |
| `suite` | `verdict`, `total`, `passed`, `failed`, `reason` |
| `heal_lap` | `lap`, `cap`, `decision` |
| `adhoc` | `reason` what the caller said the script was for, `allowed` whether the guardrail let it run, `head` its first 160 characters |

A gate's `round` is not on this list because it is not accepted: the pipeline
knows what it found and this service knows how many times it has been told, and
only one of those is a count.

### A tool call

| kind | fields |
|---|---|
| `call` | `tool`, `input`, `ok`, `seconds`, `why`, `request` |

`kind`, `target`, `signature`, `repeat` and `on_target` in the `calls` table are
worked out here from `tool` and `input`, and are not accepted from the caller
for the same reason a gate's round is not. Two pipelines counting repeats their
own way produce two numbers for one idea.

### What the CLI did to a session

| kind | fields |
|---|---|
| `compaction` | `trigger` what asked for the fold, `fold` which fold of this session it is, `pending` the rule ledger's unfilled slots at that moment, `rules` its total |
| `compression` | `comp_in`, `comp_out` characters of tool output into and out of the hook, `dedup` results served as a stub, `markers` truncation marks seen downstream, `errors` results the hook could not read, `results` how many tool results the session saw |

### Everything else

| kind | fields |
|---|---|
| `service_log` | `service`, `level`, `logger`, `text` |
| `learning` | `item`, `model`, `base`, `samples`, `epochs`, `loss`, `minutes`, `pairs`, `local_rate`, `sonnet_rate`, `total`, `fresh`, `confirmed`, `kb`, `ticket`, `transcripts`, `funcreq` |
| `heartbeat` | nothing beyond the envelope. It is a pulse, and its whole content is that it arrived |

Two kinds are written by this service about the machine rather than sent by a
pipeline, and are on the page because the rule is the rule:

| kind | fields |
|---|---|
| `sample` | `container`, `memory_mb`, `memory_pct`, `cpu_pct`, `pids` |
| `netprobe` | `host`, `latency_ms`, `ok` |

## Documents

`POST /documents` takes four fields and no others: `run`, `kind`, `phase`,
`body`.

The body is written to a file under the documents directory and the row keeps
the path, because a run's framework map is over a megabyte and a database that
swallows one of those per run is a database nobody wants to keep for a year.

**The body is stored verbatim.** Byte for byte, as sent. Nothing here strips a
key, masks a path or shortens a line. The map's own redaction, done before the
body is ever sent, is the only redaction there is, and anything a pipeline
would rather this service did not hold is a thing that has to not be sent.

## What search reads

`GET /search` indexes the prose and nothing else: the `text`, `note`, `summary`,
`error`, `reason`, `why` and `detail` of an event, plus a `call`'s tool name and
the strings it was given. Fields holding a number or an id are left out, because
a search for `3600` that returns a timeout, a token count and a session id in
one list is an answer nobody can use.

A document is indexed from its body, up to the first megabyte. The whole of it
stays on disk and `GET /documents?id=` still serves all of it; what is over a
megabyte in is findable by the words before it.

## What search costs

Indexing as the event lands is cheap and the disk is not. Ten thousand prose
events, timed and then measured on the same database: 10k posts of a heartbeat,
which indexes nothing, took 1.49s and 10k posts of a log line with 200
characters of text took 3.09s, so an indexed event costs about 0.16 ms and a
50k-event run pays eight seconds of indexing spread across its life. The space
is the number to watch. Those same events were 4.4 MB of `events` against 3.6 MB
of `search_text`, 1.4 MB of the FTS data and half a megabyte of indexes over the
two: the search structures are the size of the table they mirror, so a database
with search in it is about twice the database. The words are held once, not
twice, and it still costs that, because a row of them carries the run, the kind,
the phase and the session that make a hit worth reading. `archive` takes the
index rows out with the events they mirror, which is what stops the larger half
from being the permanent half.

The index is rebuilt rather than topped up in two places: a start that found
anything to file, and every archive. A rebuild is linear and cheap by the row,
0.030s over 10k rows and 0.78s over 318k, about 2.4 microseconds each, so a
year of runs at a million rows adds some two and a half seconds to a start and
about that again to the daily archive. It is the whole index either time, not
the part that changed, which is worth knowing before the number matters.

## Adding a field

One commit, two lines: the field in `server/record.go` and the field on this
page. Then it is stored, and until then a client that sends it is told, in the
reply to every send, that it is not.
