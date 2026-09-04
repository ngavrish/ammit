package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
)

// The page where the limits are set.
//
// They lived in a file, which is honest and unusable at the moment it matters:
// a run is going, it is costing more than it should, and the person who can
// change that is editing a compose file over somebody's shoulder. So the limits
// are fields here, next to what they are doing to the run in front of you — and
// underneath they are still the same file, so the config stays in git and a
// change made under pressure is a diff somebody can read later.
//
// Fields rather than a blob of yaml because a limit is changed in a hurry, and
// "which of these numbers is the session one" is not a question to answer in a
// hurry. The whole file is still one click away for what fields cannot express:
// adding a command, renaming a section.
//
// One page, no framework, no build step.
func pageHTML() string {
	return `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<title>ammit</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<link rel="icon" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'%3E%3Ctext y='13' font-size='13'%3E%E2%9A%96%3C/text%3E%3C/svg%3E">
<style>
/* The house style: deep navy, bronze, and mono for anything that is data.
   Fonts come from the network when there is one and fall back to what the
   machine already has when there is not — a page about a pipeline that is down
   should not itself depend on being online. */
@import url('https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;500;700&family=Plus+Jakarta+Sans:wght@400;500;600;700&display=swap');
*{box-sizing:border-box}
` + headerCSS + `
/* The page's own names, on the shell's palette: deep is a card, deeper is a
   field, bronze-dim is the line round either. */
:root{--deep:var(--navy); --panel:var(--navy); --bronze-dim:var(--hair); --ok:var(--good)}
` + footerCSS + `

h1{margin:0;font:700 1.15rem/1 var(--mono);letter-spacing:.18em;text-transform:uppercase;
   color:var(--bronze)}
main{max-width:82rem;width:100%;margin:0 auto;padding:1.5rem 1.5rem 3rem;display:grid;gap:1.25rem}
section{background:var(--deep);border:1px solid var(--bronze-dim);border-radius:12px;
        padding:1.4rem 1.6rem;box-shadow:0 1px 0 rgba(0,0,0,.25);
        animation:rise .55s var(--ease) both;
        transition:border-color .25s var(--ease),box-shadow .25s var(--ease)}
section:nth-child(2){animation-delay:60ms} section:nth-child(3){animation-delay:120ms} section:nth-child(4){animation-delay:180ms}
section:hover{border-color:var(--hair-strong)}
@keyframes rise{from{opacity:0;transform:translateY(10px)}to{opacity:1;transform:none}}
h2{margin:0 0 1.25rem;font:600 .8rem/1 var(--mono);letter-spacing:.16em;
   text-transform:uppercase;color:var(--bronze);display:flex;align-items:baseline;gap:.75rem}
h2 small{color:var(--dim);font:400 12px/1.4 var(--sans);letter-spacing:.02em;
         text-transform:none}
.hint{color:var(--dim);font-size:12px;line-height:1.4}
fieldset{border:0;border-top:1px solid var(--bronze-dim);margin:0 0 1.5rem;padding:1.1rem 0 0}
legend{color:var(--bronze);font:500 11px/1 var(--mono);letter-spacing:.22em;
       text-transform:uppercase;padding-right:.75rem}
.fields{display:grid;grid-template-columns:repeat(auto-fill,minmax(19rem,1fr));
        gap:1rem 1.75rem}
label{display:grid;gap:.3rem}
label .t{font:600 12.5px/1.3 var(--sans);color:var(--ink)}
label .k{font:500 12px/1.4 var(--mono);color:var(--mute);letter-spacing:.04em}
.field{display:flex;align-items:center;gap:.6rem}
main input,main select,main textarea{background:var(--deeper);color:var(--ink);
      border:1px solid var(--bronze-dim);border-radius:6px;padding:.5rem .7rem;
      font:400 13px/1.5 var(--mono);width:100%;
      transition:border-color var(--quick),box-shadow var(--quick),background var(--quick)}
main input:focus,main select:focus,main textarea:focus{outline:0;border-color:var(--bronze);
      box-shadow:0 0 0 2px var(--bronze-wash)}
main input.changed,main select.changed,main textarea.changed{border-color:var(--bronze);
      background:var(--bronze-wash);animation:touched .5s var(--ease)}
@keyframes touched{0%{box-shadow:0 0 0 0 var(--bronze-glow)}100%{box-shadow:0 0 0 8px rgba(205,127,50,0)}}
.as{color:var(--dim);font:400 12px/1 var(--mono);white-space:nowrap;min-width:5.5rem}
textarea{min-height:24rem;line-height:1.7;resize:vertical}
main button{background:transparent;color:var(--bronze);border:1px solid rgba(205,127,50,.45);
       border-radius:8px;padding:.55rem 1.4rem;cursor:pointer;
       font:700 12px/1 var(--mono);letter-spacing:.12em;text-transform:uppercase;
       transition:background var(--quick),color var(--quick),transform var(--quick)}
main button:hover{background:var(--bronze);color:var(--navy)}
main button:active{transform:scale(.97)}
main button.ghost{color:var(--mute);border-color:var(--hair-strong)}
main button.ghost:hover{background:var(--hair-soft);color:var(--ink)}
main button:disabled{opacity:.4;cursor:default}
.row{display:flex;gap:.9rem;align-items:center;flex-wrap:wrap}
.bar{margin-top:1.25rem;padding-top:1.25rem;border-top:1px solid var(--bronze-dim)}
.msg{color:var(--mute);font-size:12px;font-family:var(--mono);transition:color var(--quick)}
.msg.bad{color:var(--bad)} .msg.ok{color:var(--ok);animation:pop .4s var(--ease)}
@keyframes pop{from{opacity:0;transform:translateX(-6px)}to{opacity:1;transform:none}}
table{width:100%;border-collapse:collapse;font:400 13px/1.5 var(--mono)}
th{text-align:left;color:var(--bronze);font-weight:500;font-size:11px;
   letter-spacing:.14em;text-transform:uppercase;padding:.5rem .75rem;
   border-bottom:1px solid var(--bronze-dim)}
td{padding:.45rem .75rem;border-bottom:1px solid var(--hair);
   vertical-align:top;color:var(--mute);transition:background var(--quick),color var(--quick)}
tr:hover td{background:var(--bronze-wash);color:var(--ink)}
td.n{text-align:right;font-variant-numeric:tabular-nums}
.tag{padding:.15rem .55rem;border-radius:2px;font-size:11px;letter-spacing:.08em;
     text-transform:uppercase;border:1px solid}
.live{color:var(--bronze);border-color:var(--bronze-glow);background:var(--bronze-wash)}
.ok{color:var(--ok);border-color:rgba(79,169,124,.35);background:rgba(79,169,124,.08)}
.bad{color:var(--bad);border-color:rgba(224,108,90,.35);background:rgba(224,108,90,.08)}
main a{color:var(--bronze);text-decoration:none;border-bottom:1px solid var(--bronze-dim);
  font:500 12px/1 var(--mono);letter-spacing:.12em;text-transform:uppercase}
main a:hover{border-bottom-color:var(--bronze)}
[hidden]{display:none!important}
</style></head><body>
` + headerHTML("limits") + `
<div class=page>
<div id=bar><div class=title><b>Limits</b><em>the file, as fields; in force on the next tick</em></div></div>
<main id="scales">
  <section>
    <h2>Limits <small id="where"></small></h2>
    <div id="form"></div>
    <div id="raw" hidden>
      <textarea id="rawtext" spellcheck="false"></textarea>
    </div>
    <div class="row bar">
      <button id="save" onclick="save()">Save</button>
      <button class="ghost" onclick="load()">Discard changes</button>
      <button class="ghost" id="toggle" onclick="toggleRaw()">Edit the file</button>
      <span class="msg" id="msg"></span>
    </div>
  </section>
  <section>
    <h2>Queue <small>started in order, as slots free</small></h2>
    <div class="row"><input type="text" id="qname" placeholder="ticket or job name"
      style="max-width:18rem" onkeydown="if(event.key==='Enter')enqueue()">
      <button onclick="enqueue()">Queue it</button></div>
    <table id="queue"></table>
  </section>
  <section><h2>Runs</h2><table id="runs"></table></section>
  <section><h2>What ammit did <small>every limit crossed, and what followed</small></h2>
    <table id="judgements"></table></section>
</main>
<script>
// The charts load the first time somebody asks for them: a tab nobody opened
// should not be polling Grafana every thirty seconds behind the page.
const CHARTS = "{{charts}}/d/ammit/ammit?theme=dark&refresh=30s";
let earliest = 0;
// The charts are a page of their own now, not a panel that hides.
if (location.hash === "#charts") addEventListener("DOMContentLoaded", () => tab("charts"));

const j = (p) => fetch(p).then(r => r.json());
const esc = (s) => String(s ?? "").replace(/[<>&"]/g, c =>
  ({"<":"&lt;",">":"&gt;","&":"&amp;",'"':"&quot;"}[c]));
const when = (t) => t ? new Date(t*1000).toLocaleString() : "";

// What each setting is for, in the words somebody changing it under pressure
// would use. Anything not named here still gets a field — this service does not
// decide which of somebody's limits are the interesting ones.
// What each limit is called when a person reads it; the key stays the key.
const TITLES = {{titles}};
const HINT = {
  "actions.enforce": "on: limits act. off: limits still judge and write it down, but stop, retry and restart nothing - for a debug run on the subscription. start_run and warn still go.",
  "queue.parallel": "runs allowed to be going at once",
  "timeouts.run": "the whole run, start to verdict",
  "timeouts.phase": "one phase — any phase without a ceiling of its own",
  "timeouts.phase_planning": "the planning phase alone. It ran 4 to 20 minutes across six runs and 37+ on the seventh, under a 90-minute ceiling meant for implement.",
  "timeouts.session": "one agent session",
  "timeouts.turn": "silence inside one turn — the hang nothing inside the run catches",
  "limits.usd_per_run": "spend on one run",
  "limits.turns_per_run": "turns one run may take",
  "limits.spin_window": "seconds a session may keep asking the model without touching a tool",
  "limits.spin_requests": "model requests inside that window before it counts as spinning",
  "limits.net_probe_ms": "a TLS handshake to the API host slower than this is a network worth noting",
  "retention.days": "finished runs older than this move to the archive",
  "retention.dir": "where archives are written",
};
const SECONDS = /^timeouts\./;
const ORDER = ["queue", "timeouts", "limits", "retention", "actions", "commands", "context"];
const NOTE = {
  timeouts: "Seconds. Zero means no limit — and a zero somebody typed is a decision, not a gap.",
  actions: "What happens when a limit is crossed. Each names a command below.",
  commands: "{run} {name} {phase} {agent} {session} and anything under context are substituted.",
  context: "Substituted into the commands above.",
};

let conf = {}, dirty = {};

function human(seconds) {
  const n = Number(seconds);
  if (!isFinite(n) || n <= 0) return n === 0 ? "no limit" : "";
  if (n < 90) return n + " s";
  if (n < 5400) return (n/60).toFixed(n % 60 ? 1 : 0) + " min";
  return (n/3600).toFixed(n % 3600 ? 1 : 0) + " h";
}

function field(section, key, value) {
  const name = section + "." + key;
  const secs = SECONDS.test(name);
  const num = /^-?\d+(\.\d+)?$/.test(value);
  const control = name === "actions.enforce"
    ? "<select data-name='" + name + "' onchange='touch(this)'>" +
        ["on", "off"].map(c => "<option" + (c === value ? " selected" : "") + ">" + c + "</option>").join("") +
      "</select>"
    : section === "actions"
    ? "<select data-name='" + name + "' onchange='touch(this)'>" +
        Object.keys(conf.commands || {}).concat(
          Object.keys(conf.commands || {}).includes(value) ? [] : [value])
        .map(c => "<option" + (c === value ? " selected" : "") + ">" + esc(c) + "</option>")
        .join("") + "</select>"
    : "<input data-name='" + name + "' value='" + esc(value) + "'" +
      (num ? " type='number' step='any' inputmode='decimal'" : " type='text'") +
      " oninput='touch(this)'>";
  return "<label>" + (TITLES[name] ? "<span class='t'>" + esc(TITLES[name]) + "</span>" : "") +
    "<span class='k'>" + esc(key) + "</span>" +
    "<span class='field'>" + control +
    (secs ? "<span class='as' data-as='" + name + "'>" + human(value) + "</span>" : "") +
    "</span>" +
    (hintFor(name) ? "<span class='hint'>" + esc(hintFor(name)) + "</span>" : "") + "</label>";
}

// A per-phase ceiling is named after its phase, so there is no fixed list of
// them to write hints for: timeouts.phase_planning, timeouts.phase_implementing
// and whatever the next flow adds all mean the same thing about a different
// phase. Say that, rather than leaving the field the only one on the page with
// nothing next to it.
function hintFor(name) {
  if (HINT[name]) return HINT[name];
  const m = /^timeouts\.phase_(.+)$/.exec(name);
  if (m) return "the " + m[1] + " phase alone, instead of timeouts.phase";
  return "";
}

function draw() {
  const sections = Object.keys(conf).sort(
    (a, b) => (ORDER.indexOf(a) + 1 || 99) - (ORDER.indexOf(b) + 1 || 99));
  document.getElementById("form").innerHTML = sections.map(s =>
    "<fieldset><legend>" + esc(s) + "</legend>" +
    (NOTE[s] ? "<div class='hint' style='margin:-.4rem 0 .8rem'>" + esc(NOTE[s]) + "</div>" : "") +
    "<div class='fields'>" +
      Object.keys(conf[s]).sort().map(k => field(s, k, conf[s][k])).join("") +
    "</div></fieldset>").join("");
}

function touch(el) {
  const name = el.dataset.name;
  const was = conf[name.split(".")[0]][name.split(".").slice(1).join(".")];
  if (String(el.value) === String(was)) delete dirty[name]; else dirty[name] = el.value;
  el.classList.toggle("changed", name in dirty);
  const as = document.querySelector("[data-as='" + name + "']");
  if (as) as.textContent = human(el.value);
  say(Object.keys(dirty).length ? Object.keys(dirty).length + " unsaved" : "");
}

function say(text, kind) {
  const el = document.getElementById("msg");
  el.textContent = text; el.className = "msg" + (kind ? " " + kind : "");
}

async function load() {
  conf = await j("/limits");
  dirty = {};
  draw();
  document.getElementById("rawtext").value = await fetch("/limits.yml").then(r => r.text());
  document.getElementById("where").textContent = "no restart needed";
  say("");
}

async function save() {
  const raw = !document.getElementById("raw").hidden;
  const res = raw
    ? await fetch("/limits.yml", {method: "PUT",
        body: document.getElementById("rawtext").value})
    : await fetch("/limits.yml", {method: "PATCH",
        headers: {"Content-Type": "application/json"}, body: JSON.stringify(dirty)});
  if (!res.ok) { say(await res.text() || "not saved", "bad"); return; }
  await load();
  say("saved — in force on the next tick", "ok");
}

function toggleRaw() {
  const raw = document.getElementById("raw"), form = document.getElementById("form");
  const showRaw = raw.hidden;
  raw.hidden = !showRaw; form.hidden = showRaw;
  document.getElementById("toggle").textContent = showRaw ? "Back to fields" : "Edit the file";
  say(showRaw ? "the file itself — comments, sections, everything" : "");
}

async function enqueue() {
  const name = document.getElementById("qname").value.trim();
  if (!name) return;
  await fetch("/queue", {method: "POST", headers: {"Content-Type": "application/json"},
                         body: JSON.stringify({name})});
  document.getElementById("qname").value = "";
  refresh();
}

function table(el, cols, rows, cell) {
  el.innerHTML = "<tr>" + cols.map(c => "<th>" + c + "</th>").join("") + "</tr>" +
    (rows || []).map(r => "<tr>" + cell(r).map(c => "<td>" + c + "</td>").join("") + "</tr>").join("");
}

async function refresh() {
  const [runs, judgements, queue] = await Promise.all(
    [j("/runs"), j("/judgements"), j("/queue")]);
  // The charts open on the runs there are, not on a fixed twelve hours. A run
  // three hours old inside a twelve hour window is three quarters of empty
  // chart with the line squeezed against the right edge.
  const started = runs.map(r => r.started).filter(Boolean);
  if (started.length) earliest = Math.min(...started);
  table(document.getElementById("runs"),
    ["run", "started", "verdict", "minutes", "usd", "turns", "summary"], runs, r => [
      esc(r.name), when(r.started),
      "<span class='tag " + (r.verdict ? (r.verdict === "PASS" ? "ok" : "bad") : "live") + "'>" +
        esc(r.verdict || "running") + "</span>",
      Math.round(((r.finished || Date.now()/1000) - r.started) / 60),
      (r.usd || 0).toFixed(2), r.turns || 0, esc(r.summary || "")]);
  table(document.getElementById("judgements"),
    ["when", "scope", "subject", "rule", "threshold", "observed", "action", "outcome"],
    judgements, x => [when(x.at), esc(x.scope), esc(x.subject || ""), esc(x.rule),
      x.threshold ?? "", x.observed ?? "",
      "<span class='tag " + (x.action === "warn" ? "live" : "bad") + "'>" + esc(x.action) + "</span>",
      esc(x.outcome || "")]);
  table(document.getElementById("queue"), ["name", "requested", "state"], queue,
    q => [esc(q.name), when(q.requested), esc(q.state)]);
}

load(); refresh(); setInterval(refresh, 10000);
</script>` + footerHTML() + `
</div>
<script>` + headerJS + `</script>
</body></html>`
}

// serveUI wires the page and the routes it edits the config through.
func serveUI(mux *http.ServeMux, confPath, chartsURL string) {
	limits := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		page := strings.ReplaceAll(pageHTML(), "{{charts}}", chartsURL)
		io.WriteString(w, strings.ReplaceAll(page, "{{titles}}", limitTitlesJS()))
	}
	mux.HandleFunc("GET /ammit/limits", limits)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		// Every view has a path of its own; this one had the root, which said
		// nothing about which view it was.
		http.Redirect(w, r, "/ammit/limits", http.StatusFound)
	})

	mux.HandleFunc("GET /limits.yml", func(w http.ResponseWriter, r *http.Request) {
		body, err := os.ReadFile(confPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(body)
	})

	// The whole file, for what fields cannot say: a new command, a renamed
	// section, a comment explaining why a number is what it is.
	mux.HandleFunc("PUT /limits.yml", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Refuse a file the server would then fail to read: a limits file that
		// does not parse is a pipeline with no limits, and finding that out on
		// the next tick is too late.
		if len(parseConfig(string(body))) == 0 {
			http.Error(w, "that does not parse as limits", http.StatusBadRequest)
			return
		}
		if err := os.WriteFile(confPath, body, 0o644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("ammit: limits replaced through the page (%d bytes)", len(body))
		w.WriteHeader(http.StatusNoContent)
	})

	// One field at a time: {"timeouts.run": "7200"}. Values are edited in place,
	// so the comments and the order somebody wrote survive being changed by
	// somebody else in a hurry.
	mux.HandleFunc("PATCH /limits.yml", func(w http.ResponseWriter, r *http.Request) {
		var values map[string]string
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&values); err != nil {
			http.Error(w, "not json", http.StatusBadRequest)
			return
		}
		if len(values) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		before, err := os.ReadFile(confPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		after, err := setValues(string(before), values)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(parseConfig(after)) == 0 {
			http.Error(w, "that does not parse as limits", http.StatusBadRequest)
			return
		}
		if err := os.WriteFile(confPath, []byte(after), 0o644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, name := range sortedKeys(values) {
			log.Printf("ammit: %s set to %s through the page", name, values[name])
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// setValues rewrites named settings in the file and leaves everything else
// exactly as it was — indentation, blank lines, and above all the comments,
// which are usually the only record of why a limit is the number it is. A
// setting the file does not have yet is added to its section, or the section is
// added at the end.
func setValues(text string, values map[string]string) (string, error) {
	for name, value := range values {
		if !strings.Contains(name, ".") {
			return "", errNamed(name)
		}
		if strings.ContainsAny(value, "\n\r") {
			return "", errNamed(name)
		}
	}
	lines := strings.Split(text, "\n")
	section := ""
	done := map[string]bool{}

	for i, line := range lines {
		body, comment := cutComment(line)
		if strings.TrimSpace(body) == "" {
			continue
		}
		if !strings.HasPrefix(body, " ") && !strings.HasPrefix(body, "\t") {
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(body), ":"))
			continue
		}
		key, _, found := strings.Cut(strings.TrimSpace(body), ":")
		if !found {
			continue
		}
		name := section + "." + strings.TrimSpace(key)
		value, wanted := values[name]
		if !wanted {
			continue
		}
		indent := body[:len(body)-len(strings.TrimLeft(body, " \t"))]
		// The gap before a trailing comment is kept, so a column of aligned
		// comments stays a column.
		gap := ""
		if comment != "" {
			gap = strings.Repeat(" ", max(1, len(body)-len(strings.TrimRight(body, " "))))
		}
		lines[i] = indent + strings.TrimSpace(key) + ": " + quoted(value) + gap + comment
		done[name] = true
	}

	for _, name := range sortedKeys(values) {
		if done[name] {
			continue
		}
		want, key, _ := strings.Cut(name, ".")
		lines = insertInto(lines, want, "  "+key+": "+quoted(values[name]))
	}
	return strings.Join(lines, "\n"), nil
}

// insertInto puts a line at the end of a section, creating the section if the
// file has never had one.
func insertInto(lines []string, want, line string) []string {
	section, last := "", -1
	for i, l := range lines {
		body, _ := cutComment(l)
		if strings.TrimSpace(body) == "" {
			continue
		}
		if !strings.HasPrefix(body, " ") && !strings.HasPrefix(body, "\t") {
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(body), ":"))
			continue
		}
		if section == want {
			last = i
		}
	}
	if last < 0 {
		return append(lines, "", want+":", line)
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:last+1]...)
	out = append(out, line)
	return append(out, lines[last+1:]...)
}

// quoted keeps a value that would confuse the reader — a comment marker, a
// leading or trailing space — as a quoted string.
func quoted(value string) string {
	if value == "" {
		return `""`
	}
	if body, comment := cutComment(value); comment != "" || body == "" ||
		strings.TrimSpace(value) != value {
		return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
	}
	return value
}

type errNamed string

func (e errNamed) Error() string { return "cannot set " + string(e) }

// parseConfig is loadConfig for text that is not on disk yet.
func parseConfig(raw string) Config {
	conf := Config{}
	section := ""
	for _, line := range strings.Split(raw, "\n") {
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
