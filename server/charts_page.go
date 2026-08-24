package main

import _ "embed"

// The page. uPlot is vendored rather than fetched: a chart that needs the
// internet is a chart that is blank on the machine that most needs it.

//go:embed uPlot.iife.min.js
var uplotJS string

//go:embed uPlot.min.css
var uplotCSS string

func chartsPageHTML(active string) string { return `<!doctype html><meta charset="utf-8">
<title>ammit — charts</title>
<style>` + uplotCSS + `
/* Light, on the palette the rest of Chiron uses: bronze against navy text on
   white, with the greys that sit between them. The charts were navy-on-navy and
   the header had grown one control at a time until the row wrapped into itself. */
:root{
  --bronze:#CD7F32; --bronze-wash:rgba(205,127,50,.10);
  --navy:#001F3F; --ink:#0F1520; --slate:#4A5568; --mute:#A0AEC0;
  --hair:#E5E7EB; --hair-soft:#F1F3F6; --ground:#FFFFFF; --raised:#FAFBFC;
  --sky:#0EA5E9; --good:#22C55E; --bad:#EF4444; --warm:#FB923C;
  --arrow:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 13 9'%3E%3Cpath d='M4.8.7 1 4.5l3.8 3.8M1 4.5h11' fill='none' stroke='%23000' stroke-width='1.6' stroke-linecap='round' stroke-linejoin='round'/%3E%3C/svg%3E");
  --lens:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'%3E%3Cg fill='none' stroke='%23000' stroke-width='1.9' stroke-linecap='round'%3E%3Ccircle cx='6.8' cy='6.8' r='4.7'/%3E%3Cpath d='M10.4 10.4 14 14'/%3E%3C/g%3E%3C/svg%3E");
  --navy-bar:#001F3F; --on-bar:#F7FAFC; --on-bar-dim:#A0AEC0;
  --bar-hair:rgba(205,127,50,.28); --bar-soft:rgba(247,250,252,.07);
  --mono:"JetBrains Mono",ui-monospace,SFMono-Regular,Menlo,monospace;
  --sans:"Plus Jakarta Sans","Inter",ui-sans-serif,system-ui,-apple-system,sans-serif;
}
*{box-sizing:border-box}
body{margin:0;background:var(--ground);color:var(--ink);font:14px/1.55 var(--sans)}

` + headerCSS + `
#bar{position:sticky;top:81px;z-index:25;padding:9px 24px;
  background:var(--raised);border-bottom:1px solid var(--hair);
  display:flex;align-items:center;gap:12px;flex-wrap:wrap}
#tz{font:400 12px/1 var(--mono);color:var(--slate);background:var(--ground);
  border:1px solid var(--hair);border-radius:8px;padding:7px 9px;cursor:pointer}
#tz:hover{border-color:var(--bronze);color:var(--bronze)}
#bar>button{font:inherit;font-size:12.5px;color:var(--ink);
  background:var(--ground);border:1px solid var(--hair);border-radius:8px;
  padding:7px 11px;cursor:pointer}
#bar>button:hover{border-color:var(--bronze);color:var(--bronze)}

/* How long a window is, as the four lengths anybody picks. A dropdown hid three
   of them and made the fourth something you had to open a menu to read — the same
   thing the tabs above stopped doing. */
#range{display:flex;align-items:center;gap:0;border:1px solid var(--hair);
  border-radius:8px;overflow:hidden;background:var(--ground)}
.span{border:0;border-right:1px solid var(--hair);background:none;cursor:pointer;
  padding:7px 13px;color:var(--slate);
  font:400 12.5px/1 var(--mono);transition:color .12s,background .12s}
.span:last-child{border-right:0}
.span:hover{color:var(--bronze)}
.span.on{background:var(--bronze);color:#001F3F;font-weight:700}

/* And how often to ask again. Off by default: a page that reloads under you is
   a page you cannot read a chart on, and most of the time nothing is running. */
#every{display:flex;align-items:center;gap:0;border:1px solid var(--hair);
  border-radius:8px;overflow:hidden;background:var(--ground)}
.tick{border:0;border-right:1px solid var(--hair);background:none;cursor:pointer;
  padding:7px 11px;color:var(--mute);
  font:400 12px/1 var(--mono);transition:color .12s,background .12s}
.tick:last-child{border-right:0}
.tick:hover{color:var(--bronze)}
.tick.on{background:var(--hair-soft);color:var(--navy);font-weight:700}
.tick.on[data-s="0"]{color:var(--mute);font-weight:400}

/* One control rather than six sitting next to each other. */
#filters{display:flex;align-items:center;gap:0;flex-wrap:wrap;
  border:1px solid var(--hair);border-radius:8px;background:var(--ground);
  padding:2px;overflow:hidden}
#filters input,#filters button{border:0;background:none;border-radius:6px;
  padding:6px 10px;font:inherit;font-size:12.5px;color:var(--ink)}
#filters input::placeholder{color:var(--mute)}
#filters input:focus{outline:0;background:var(--bronze-wash)}

/* The ticket is what anybody types first and it was the smallest thing on the
   strip. A lens in front of it, room for a whole ticket key, and the type that
   ticket keys are written in. */
.find{display:flex;align-items:center;padding-left:10px;border-radius:7px}
.find::before{content:"";width:13px;height:13px;flex:none;opacity:.5;
  background:var(--slate);
  -webkit-mask:var(--lens) center/contain no-repeat;
  mask:var(--lens) center/contain no-repeat}
.find:focus-within{background:var(--bronze-wash);
  box-shadow:0 0 0 1px rgba(205,127,50,.35)}
.find:focus-within::before{background:var(--bronze);opacity:1}
#q{font:600 13.5px/1 var(--mono);letter-spacing:.03em;padding:9px 14px 9px 8px;
  text-transform:uppercase;min-width:230px}
#q::placeholder{font-weight:400;letter-spacing:.02em;text-transform:none}
#q:focus{background:none}
#filters input[type=date]{font:11.5px/1 var(--mono);color:var(--slate);
  padding:6px;min-width:116px}
.sep{width:1px;align-self:stretch;background:var(--hair);margin:3px 2px}
.when{display:flex;align-items:center;gap:2px}
.when i{font:600 9.5px/1 var(--sans);font-style:normal;letter-spacing:.1em;
  text-transform:uppercase;color:var(--mute);padding:0 6px 0 9px}
#clear{color:var(--mute);cursor:pointer}
#clear:hover{color:var(--bad)}

#note{margin-left:auto;color:var(--slate);font:600 11px/1 var(--sans);
  letter-spacing:.06em;text-transform:uppercase;font-variant-numeric:tabular-nums}

main{padding:20px 22px 72px;display:flex;flex-direction:column;gap:26px}
.row{font:700 11px/1 var(--sans);letter-spacing:.14em;text-transform:uppercase;
  color:var(--bronze);border-bottom:1px solid var(--hair);padding-bottom:8px;margin-top:12px}
.panel h2{font:700 14px/1.3 var(--sans);margin:0 0 3px;color:var(--navy)}
.panel p{margin:0 0 10px;color:var(--slate);font-size:12px;max-width:92ch}
/* Height for a plot, not for a message. A panel with nothing in this window
   holds one line of text, and reserving two thirds of the screen under it turns
   an empty window into a page of scrolling. */
.plot{min-height:0}
.plot.drawn{min-height:360px}

table{border-collapse:collapse;width:100%;font:12px/1.5 var(--mono);display:block;
  overflow:auto;max-height:360px;border:1px solid var(--hair);border-radius:6px}
th,td{border-bottom:1px solid var(--hair-soft);padding:6px 11px;text-align:left;
  white-space:nowrap;font-variant-numeric:tabular-nums}
th{color:var(--slate);position:sticky;top:0;background:var(--raised);
  font-family:var(--sans);font-weight:700;font-size:11px;letter-spacing:.04em;
  text-transform:uppercase}
tbody tr:hover{background:var(--bronze-wash)}
.err{color:var(--bad);font-size:12px}
.empty{color:var(--mute);font-size:12px;padding:8px 0}
.u-legend{font-size:11px;color:var(--slate)}

.tiles{display:grid;gap:14px;grid-template-columns:repeat(auto-fill,minmax(258px,1fr))}
.tile{border:1px solid var(--hair);border-left:3px solid var(--mute);border-radius:8px;
  padding:13px 15px;cursor:pointer;background:var(--ground);transition:.12s}
.tile:hover{border-color:var(--hair);border-left-color:var(--bronze);
  box-shadow:0 2px 10px rgba(15,21,32,.07);transform:translateY(-1px)}
.tile.green{border-left-color:var(--good)}
.tile.red{border-left-color:var(--bad)}
.tile.going{border-left-color:var(--warm)}
.tile b{font:700 14px/1.3 var(--sans);display:block;color:var(--navy)}
.tile .verdict{font:700 10px/1.6 var(--sans);letter-spacing:.11em;
  text-transform:uppercase;color:var(--slate)}
.tile.green .verdict{color:var(--good)}
.tile.red .verdict{color:var(--bad)}
.tile.going .verdict{color:var(--warm)}
.tile dl{display:grid;grid-template-columns:auto 1fr;gap:3px 12px;margin:10px 0 0;
  font:11.5px/1.5 var(--mono);font-variant-numeric:tabular-nums}
.tile dt{color:var(--mute);font-family:var(--sans)}
.tile dd{margin:0;text-align:right;color:var(--ink)}
.back{color:var(--bronze);cursor:pointer;font-size:12.5px;font-weight:600;
  display:inline-block;margin-bottom:4px}
.back:hover{text-decoration:underline}
` + footerCSS + `
</style>
` + headerHTML(active) + `

<div id=bar>
  <div id=filters>
    <span class=find>
      <input id=q placeholder="APF-1934" size=22 autocomplete=off spellcheck=false>
    </span>
    <span class=sep></span>
    <label class=when><i>started</i><input id=sf type=date><input id=st type=date></label>
    <span class=sep></span>
    <label class=when><i>finished</i><input id=ff type=date><input id=ft type=date></label>
    <button id=clear title="clear every filter">clear</button>
  </div>
  <nav id=range>
    <button class=span data-h=3>3 hours</button>
    <button class="span on" data-h=12>12 hours</button>
    <button class=span data-h=48>2 days</button>
    <button class=span data-h=168>a week</button>
  </nav>
  <nav id=every title="how often to run every query again">
    <button class="tick on" data-s=0>manual</button>
    <button class=tick data-s=5>5s</button>
    <button class=tick data-s=300>5m</button>
    <button class=tick data-s=1800>30m</button>
    <button class=tick data-s=3600>1h</button>
  </nav>
  <button id=refresh title="run every query again now">refresh</button>
  <select id=tz title="which clock the times are read in"></select>
  <span id=note></span>
</div>

<main id=main></main>
` + footerHTML() + `
<script>` + uplotJS + `</script>
<script>
const COLORS=["#CD7F32","#0EA5E9","#22C55E","#FB923C","#EF4444","#8B5CF6",
  "#0891B2","#CA8A04","#2563EB","#DB2777"];
const main=document.getElementById("main");
let panels=[];

// Which clock the axis is read in. uPlot draws in whatever the browser thinks
// local is, and a browser in a container thinks UTC — so a run at seven in the
// evening was labelled five, with nothing on the page saying which.
const HERE=Intl.DateTimeFormat().resolvedOptions().timeZone||"UTC";
const ZONES=[...new Set([HERE,"UTC","Europe/Berlin","Europe/Paris","Europe/London",
  "Europe/Moscow","America/New_York","America/Los_Angeles","Asia/Tokyo"])];
let zone = localStorage.getItem("ammit.tz") || HERE;

function fillZones(){
  const sel=document.getElementById("tz");
  sel.innerHTML=ZONES.map(z=>'<option value="'+z+'"'+(z===zone?" selected":"")+'>'+
    z.replace(/_/g," ")+(z===HERE?" (this machine)":"")+'</option>').join("");
  sel.onchange=()=>{ zone=sel.value; localStorage.setItem("ammit.tz",zone); load(); };
}

// uPlot hands the axis a moment and asks for a label; this reads it in the
// chosen zone rather than the browser's.
function tzFmt(){
  const day=new Intl.DateTimeFormat("en-GB",{timeZone:zone,day:"2-digit",month:"short"});
  const min=new Intl.DateTimeFormat("en-GB",{timeZone:zone,hour:"2-digit",minute:"2-digit",hour12:false});
  return (u,splits)=>splits.map((t,i)=>{
    const d=new Date(t*1000);
    const hm=min.format(d);
    return (i===0||hm==="00:00") ? hm+"\n"+day.format(d) : hm;
  });
}

function hours(){
  const on=document.querySelector(".span.on");
  return on ? +on.dataset.h : 12;
}

// Open on a window that holds something.
//
// Twelve hours was the default and the last run was the day before yesterday, so
// the page opened empty and three of the four lengths were indistinguishable from
// each other — every one of them showed nothing, so switching between them did
// nothing. The smallest window that reaches the newest run is the one worth
// opening on.
let pickedWindow=false;
async function windowForNewest(){
  if(pickedWindow) return; pickedWindow=true;
  try{
    const rows=await (await fetch("/charts/runs")).json();
    if(!rows.length) return;
    const ageH=(Date.now()/1000 - rows[0].started)/3600;
    const fits=[...document.querySelectorAll(".span")]
      .map(b=>+b.dataset.h).sort((a,b)=>a-b).find(h=>h>ageH);
    const want=fits||168;
    document.querySelectorAll(".span").forEach(x=>
      x.classList.toggle("on", +x.dataset.h===want));
  }catch(e){/* leave the default */}
}
let runs=[], scope="runs", chosen="";

// A verdict is a word this pipeline chose, and there are several for each of the
// two outcomes. Colour is about which of the two it was.
function shade(r){
  if(!r.finished) return "going";
  const v=(r.verdict||"").toUpperCase();
  if(/PASS|GREEN|DONE|APPROVE|SHIPPED/.test(v)) return "green";
  if(!v) return "";
  return "red";
}
function ago(sec){
  if(!sec) return "—";
  const m=Math.round(sec/60);
  if(m<60) return m+" min";
  const h=Math.floor(m/60); return h+"h "+(m%60)+"m";
}
function when(t){return new Date(t*1000).toLocaleString()}

// Picking a run sends the run, and the server narrows every table to it. The
// window still travels because some queries draw against it, and it is opened
// wide enough to hold the whole run.
function windowMs(){
  if(chosen){
    const r=runs.find(x=>x.run===chosen);
    if(r) return [r.started*1000-2000, (r.finished||Date.now()/1000)*1000+2000];
  }
  const to=Date.now();return[to-hours()*3600e3,to];
}
function runParam(){ return chosen?"&run="+encodeURIComponent(chosen):"" }

// The series come back as rows of (time, metric, value) — one long table, many
// lines. uPlot wants a column per line over one shared clock, so the rows are
// pivoted here and gaps are left as nulls rather than joined across.
// Seconds or milliseconds, whichever the query happened to return.
//
// Most of them select e.at, which is unix seconds; the window travels in
// milliseconds because that is what Grafana's macros were written in. Dividing
// everything by a thousand sent the seconds to 1970, and while the scale
// followed the data that was invisible — the axis was wrong and the line was
// there. Pinned to the window, the line left the chart entirely.
const secs = t => t > 1e12 ? t/1000 : t;

function pivot(cols,rows){
  const ti=cols.indexOf("time"),mi=cols.indexOf("metric"),vi=cols.indexOf("value");
  if(ti<0||vi<0) return null;
  const times=[...new Set(rows.map(r=>secs(+r[ti])))].sort((a,b)=>a-b);
  const at=new Map(times.map((t,i)=>[t,i]));
  const names=mi<0?["value"]:[...new Set(rows.map(r=>String(r[mi])))];
  const data=[times,...names.map(()=>new Array(times.length).fill(null))];
  for(const r of rows){
    const i=at.get(secs(+r[ti])); const j=mi<0?0:names.indexOf(String(r[mi]));
    if(i!=null&&j>=0) data[j+1][i]=r[vi]==null?null:+r[vi];
  }
  return {data,names};
}

function drawSeries(box,payload){
  const s=payload.series[0]||{};
  if(s.error){box.classList.remove("drawn");box.innerHTML='<div class=err>'+s.error+'</div>';return}
  const p=pivot(s.columns||[],s.rows||[]);
  box.classList.toggle("drawn", !!(p&&p.data[0].length));
  if(!p||!p.data[0].length){box.innerHTML='<div class=empty>nothing in this window</div>';return}
  const opts={
    width:box.clientWidth||900,
    // Two thirds of the window, whatever the panel asked for. A time series
    // squeezed into two hundred pixels is a line that goes up: the shape is the
    // whole point of drawing it, and the shape needs room.
    height:Math.max(360, Math.round(innerHeight*0.66)),
    // The window that was asked for, not the extent of whatever came back. Some
    // queries return every point of a run that merely overlaps the window, so a
    // chart labelled three hours was drawing twenty-four and saying so along its
    // own axis.
    scales:{x:{time:true,range:()=>[payload.from/1000, payload.to/1000]}},
    axes:[{stroke:"#4A5568",grid:{stroke:"#F1F3F6"},ticks:{stroke:"#E5E7EB"},size:44,
           values:tzFmt()},
          // Wide enough for the number. Left at its default the axis cut its own
          // labels — a chart of turns per run read "0,000" down the side.
          {stroke:"#4A5568",grid:{stroke:"#F1F3F6"},ticks:{stroke:"#E5E7EB"},size:74,
           values:(u,vs)=>vs.map(v=>v==null?"":
             Math.abs(v)>=1e6 ? (v/1e6).toFixed(1)+"M" :
             Math.abs(v)>=1e3 ? (v/1e3).toFixed(v%1e3?1:0)+"k" : v)}],
    series:[{},...p.names.map((n,i)=>({
      label:n,stroke:COLORS[i%COLORS.length],width:1.4,
      // A limit is drawn as it behaves: flat until it is changed.
      ...(/limit|cap|ceiling/i.test(n)?{dash:[6,4],width:1}:{})
    }))],
  };
  box.innerHTML="";
  new uPlot(opts,p.data,box);
}

function drawTable(box,payload){
  const s=payload.series[0]||{};
  if(s.error){box.innerHTML='<div class=err>'+s.error+'</div>';return}
  if(!(s.rows||[]).length){box.innerHTML='<div class=empty>nothing in this window</div>';return}
  const th=(s.columns||[]).map(c=>"<th>"+c+"</th>").join("");
  const tr=s.rows.slice(0,300).map(r=>"<tr>"+r.map(c=>"<td>"+
    (c==null?"":String(c)).replace(/[<&]/g,x=>x==="<"?"&lt;":"&amp;")+"</td>").join("")+"</tr>").join("");
  box.innerHTML="<table><thead><tr>"+th+"</tr></thead><tbody>"+tr+"</tbody></table>";
}

async function load(){
  const [from,to]=windowMs();
  const q=(scope==="lifetime"?"&scope=lifetime":"")+runParam();
  const note=document.getElementById("note");
  let drawn=0;
  await Promise.all(panels.map(async (p,i)=>{
    if(p.kind==="row") return;
    const box=document.getElementById("plot-"+i);
    if(!box) return;
    try{
      const r=await fetch("/charts/data?panel="+i+"&from="+from+"&to="+to+q);
      const payload=await r.json();
      (p.kind==="table"?drawTable:drawSeries)(box,payload);
      drawn++;
    }catch(e){ box.innerHTML='<div class=err>'+e+'</div>' }
  }));
  const withData=[...document.querySelectorAll(".plot")]
    .filter(b=>b.classList.contains("drawn")||b.querySelector("table")).length;
  note.textContent = withData===drawn ? drawn+" panels"
    : withData+" of "+drawn+" panels have data";
  sink();
}

function filters(){
  const day=id=>{const v=document.getElementById(id).value;
    return v?Math.floor(new Date(v).getTime()/1000):""};
  const p=new URLSearchParams();
  const n=document.getElementById("q").value.trim();
  if(n) p.set("name",n);
  const sf=day("sf"), st=day("st"), ff=day("ff"), ft=day("ft");
  if(sf) p.set("started_from",sf);
  if(st) p.set("started_to",st+86399);
  if(ff) p.set("finished_from",ff);
  if(ft) p.set("finished_to",ft+86399);
  return p.toString();
}

async function fillRuns(){
  runs=await (await fetch("/charts/runs?"+filters())).json();
}

function drawTiles(){
  document.getElementById("note").textContent=runs.length+" runs";
  if(!runs.length){
    main.innerHTML='<div class=empty>no run matches that</div>';return;
  }
  main.innerHTML='<div class=tiles>'+runs.map(r=>
    '<div class="tile '+shade(r)+'" data-run="'+r.run+'">'+
      '<b>'+(r.name||r.run)+'</b>'+
      '<span class=verdict>'+(r.finished?(r.verdict||"no verdict"):"running")+'</span>'+
      '<dl>'+
        '<dt>started</dt><dd>'+when(r.started)+'</dd>'+
        '<dt>took</dt><dd>'+(r.finished?ago(r.finished-r.started):ago(Date.now()/1000-r.started))+'</dd>'+
        '<dt>turns</dt><dd>'+(r.turns||0)+'</dd>'+
        '<dt>cost</dt><dd>$'+(r.usd||0).toFixed(2)+'</dd>'+
      '</dl></div>').join("")+'</div>';
  main.querySelectorAll(".tile").forEach(t=>t.onclick=()=>go("/ammit/runs/"+t.dataset.run));
}

// Panels with nothing in them go to the bottom, together, under a line. Left
// where they are, they put a gap between every pair of charts and reading the
// page became scrolling past what is not there.
function sink(){
  const empties=[...document.querySelectorAll(".panel")]
    .filter(p=>{const b=p.querySelector(".plot");
                return b && !b.classList.contains("drawn") && !b.querySelector("table")});
  document.getElementById("nothing")?.remove();
  if(!empties.length) return;
  const rule=document.createElement("div");
  rule.id="nothing"; rule.className="row";
  rule.textContent="Nothing in this window ("+empties.length+")";
  main.appendChild(rule);
  empties.forEach(p=>main.appendChild(p));
}

async function boot(){
  if(!document.getElementById("tz").options.length) fillZones();
  // The filter group collapses whole, so the row cannot half-empty and reflow.
  const picking = scope==="runs" && !chosen;
  document.getElementById("filters").style.display=picking?"flex":"none";
  document.getElementById("range").style.display=scope==="window"?"flex":"none";
  if(scope==="window") await windowForNewest();
  if(picking){ await fillRuns(); drawTiles(); return; }
  if(scope==="runs") await fillRuns();
  panels=(await (await fetch("/charts/panels"+(scope==="lifetime"?"?scope=lifetime":""))).json()).panels;
  const r=runs.find(x=>x.run===chosen);
  const head=r?'<div class=back id=back>← every run</div><div class=row>'+
    (r.name||r.run)+" — "+when(r.started)+" — "+(r.verdict||"running")+
    " — "+(r.turns||0)+" turns — $"+(r.usd||0).toFixed(2)+'</div>':"";
  main.innerHTML=head+panels.map((p,i)=>p.kind==="row"
    ? '<div class=row>'+p.title+'</div>'
    : '<section class=panel><h2>'+p.title+'</h2>'+
      (p.about?'<p>'+p.about.replace(/[<&]/g,x=>x==="<"?"&lt;":"&amp;")+'</p>':'')+
      '<div class=plot id=plot-'+i+'></div></section>').join("");
  const back=document.getElementById("back");
  if(back) back.onclick=()=>go("/ammit/runs");
  await load();
}
document.getElementById("refresh").onclick=load;

// Asking again on a timer. One timer, replaced rather than stacked: setting an
// interval without clearing the last one is how a page ends up making four
// requests a second and nobody can say why.
let ticking=0;
document.querySelectorAll(".tick").forEach(b=>b.onclick=()=>{
  document.querySelectorAll(".tick").forEach(x=>x.classList.toggle("on",x===b));
  clearInterval(ticking);
  const s=+b.dataset.s;
  if(s>0) ticking=setInterval(load, s*1000);
});
document.querySelectorAll(".span").forEach(b=>b.onclick=()=>{
  document.querySelectorAll(".span").forEach(x=>x.classList.toggle("on",x===b));
  load();
});
// The tabs are links: they work with the middle button, they can be copied, and
// a keyboard reaches them. The handler only spares the page a reload.
document.querySelectorAll(".tab[data-scope]").forEach(t=>t.onclick=e=>{
  e.preventDefault(); go(t.getAttribute("href"));
});
["q","sf","st","ff","ft"].forEach(id=>{
  const el=document.getElementById(id);
  el.oninput=()=>{clearTimeout(window._f);window._f=setTimeout(boot,300)};
});
document.getElementById("clear").onclick=()=>{
  ["q","sf","st","ff","ft"].forEach(id=>document.getElementById(id).value="");
  boot();
};
addEventListener("resize",()=>{clearTimeout(window._t);window._t=setTimeout(load,250)});
// The address says where you are: /charts#window, /charts#lifetime, and a run by
// its id. A view you cannot send anybody is a view you have to describe instead.
// The address is the state. Nothing else holds it: a click writes the hash, the
// hash draws the page. Keeping a copy beside it is what made the page fight
// itself — one wrote, the other read what was there a moment ago, and every
// switch landed on the view you had just left.
function go(path){
  if(location.pathname === path){ render(); return; }
  history.pushState(null,"",path);
  render();
}

async function render(){
  const parts=location.pathname.split("/").filter(Boolean);   // ammit, view, id?
  const view=parts[1]||"runs";
  chosen = view==="runs" && parts[2] ? parts[2] : "";
  scope  = ["window","lifetime"].includes(view) ? view : "runs";
  document.querySelectorAll(".tab").forEach(x=>
    x.classList.toggle("on", !chosen && x.dataset.scope===scope));
  await boot();
}

addEventListener("popstate", render);
render();
</script>` }
