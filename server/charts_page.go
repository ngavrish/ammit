package main

import _ "embed"

// The page. uPlot is vendored rather than fetched: a chart that needs the
// internet is a chart that is blank on the machine that most needs it.

//go:embed uPlot.iife.min.js
var uplotJS string

//go:embed uPlot.min.css
var uplotCSS string

func chartsPageHTML() string { return `<!doctype html><meta charset="utf-8">
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
  --sans:"Plus Jakarta Sans",system-ui,-apple-system,sans-serif;
}
*{box-sizing:border-box}
body{margin:0;background:var(--ground);color:var(--ink);font:14px/1.55 var(--sans)}

/* One row that does not reflow when a control is hidden: the filters live in
   their own group and the group collapses whole. */
/* Three columns, so the mark is centred against the bar rather than against
   whatever happens to be beside it. Back on the left, the company in the middle,
   where you are on the right.
 *
 * No product name: the tab that is lit says which of them you are looking at,
 * and the name was taking the widest part of the bar to repeat it. */
header{position:sticky;top:0;z-index:30;padding:0 24px;height:60px;
  background:var(--navy-bar);border-bottom:1px solid var(--bar-hair);
  display:grid;grid-template-columns:1fr auto 1fr;align-items:center;gap:16px}

.home{justify-self:start;display:inline-flex;align-items:center;gap:8px;
  text-decoration:none;padding:7px 14px 7px 11px;border-radius:20px;
  border:1px solid var(--bar-hair);background:var(--bar-soft);
  font:700 12px/1 var(--sans);letter-spacing:.03em;color:#F7FAFC;
  transition:background .14s,border-color .14s}
.home::before{content:"";width:13px;height:9px;flex:none;background:var(--bronze);
  -webkit-mask:var(--arrow) center/contain no-repeat;
  mask:var(--arrow) center/contain no-repeat;transition:transform .14s}
.home:hover{background:rgba(205,127,50,.14);border-color:var(--bronze)}
.home:hover::before{transform:translateX(-3px)}

.brand{justify-self:center;display:flex;align-items:center;gap:9px}
.brand-text{display:flex;flex-direction:column;line-height:1}
.brand-text b{font:800 12px/1 var(--sans);letter-spacing:.17em;color:#F7FAFC}
.brand-text small{font:600 8px/1.5 var(--sans);letter-spacing:.31em;color:var(--bronze)}

#tabs{margin-left:auto;display:flex;gap:2px;background:var(--bar-soft);
  border:1px solid var(--bar-hair);border-radius:9px;padding:3px;flex:none}
.tab{border:0;background:none;border-radius:6px;padding:7px 15px;
  color:var(--on-bar-dim);font:600 12.5px/1 var(--sans);cursor:pointer;
  text-decoration:none;display:inline-flex;align-items:center}
.tab:hover{color:#F7FAFC}
.tab.on{background:var(--bronze);color:#001F3F}

/* The strip: light, sticky under the bar, and it holds everything with a
   control on it. */
#bar{position:sticky;top:60px;z-index:25;padding:9px 24px;
  background:var(--raised);border-bottom:1px solid var(--hair);
  display:flex;align-items:center;gap:12px;flex-wrap:wrap}
#bar select,#bar>button{font:inherit;font-size:12.5px;color:var(--ink);
  background:var(--ground);border:1px solid var(--hair);border-radius:8px;
  padding:7px 11px;cursor:pointer}
#bar>button:hover,#bar select:hover{border-color:var(--bronze);color:var(--bronze)}

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
#q{font:600 13.5px/1 var(--mono);letter-spacing:.03em;padding:9px 12px 9px 8px;
  text-transform:uppercase}
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
.plot{min-height:60px}

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
<header>
  <a class=home href="https://dokimos.chiron.systems">back</a>

  <span class=brand aria-hidden=true>
    <svg width="30" height="27" viewBox="0 0 180 160">
      <defs>
        <linearGradient id="brandArrow" x1="0%" y1="0%" x2="100%" y2="0%">
          <stop offset="0%" stop-color="#CD7F32" stop-opacity="0"/>
          <stop offset="40%" stop-color="#CD7F32" stop-opacity=".4"/>
          <stop offset="85%" stop-color="#CD7F32" stop-opacity=".9"/>
          <stop offset="100%" stop-color="#CD7F32"/>
        </linearGradient>
        <mask id="brandCut">
          <rect width="180" height="160" fill="#fff"/>
          <line x1="5" y1="80" x2="180" y2="80" stroke="#000" stroke-width="15" stroke-linecap="round"/>
        </mask>
      </defs>
      <g transform="rotate(-30 90 80)">
        <path d="M 58 18 C 115 20, 150 45, 150 80 C 150 115, 115 140, 58 142
                 C 105 135, 128 110, 128 80 C 128 50, 105 25, 58 18 Z"
              fill="#F7FAFC" mask="url(#brandCut)"/>
        <path d="M 10 80 L 165 77.5 L 175 80 L 165 82.5 Z" fill="url(#brandArrow)"/>
      </g>
    </svg>
    <span class=brand-text><b>CHIRON</b><small>SYSTEMS</small></span>
  </span>

  <nav id=tabs>
    <a class=tab href="/" title="the limits every run is judged against, and the queue">Limits</a>
    <button class="tab on" data-scope=runs>Runs</button>
    <button class=tab data-scope=window>A window</button>
    <button class=tab data-scope=lifetime>All time</button>
  </nav>
</header>

<div id=bar>
  <div id=filters>
    <span class=find>
      <input id=q placeholder="APF-1934" size=13 autocomplete=off spellcheck=false>
    </span>
    <span class=sep></span>
    <label class=when><i>started</i><input id=sf type=date><input id=st type=date></label>
    <span class=sep></span>
    <label class=when><i>finished</i><input id=ff type=date><input id=ft type=date></label>
    <button id=clear title="clear every filter">clear</button>
  </div>
  <select id=range>
    <option value=3>last 3 hours</option>
    <option value=12 selected>last 12 hours</option>
    <option value=48>last 2 days</option>
    <option value=168>last week</option>
  </select>
  <button id=refresh title="run every query again">refresh</button>
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

function hours(){return +document.getElementById("range").value}
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
function pivot(cols,rows){
  const ti=cols.indexOf("time"),mi=cols.indexOf("metric"),vi=cols.indexOf("value");
  if(ti<0||vi<0) return null;
  const times=[...new Set(rows.map(r=>+r[ti]))].sort((a,b)=>a-b);
  const at=new Map(times.map((t,i)=>[t,i]));
  const names=mi<0?["value"]:[...new Set(rows.map(r=>String(r[mi])))];
  const data=[times,...names.map(()=>new Array(times.length).fill(null))];
  for(const r of rows){
    const i=at.get(+r[ti]); const j=mi<0?0:names.indexOf(String(r[mi]));
    if(i!=null&&j>=0) data[j+1][i]=r[vi]==null?null:+r[vi];
  }
  return {data,names};
}

function drawSeries(box,payload){
  const s=payload.series[0]||{};
  if(s.error){box.innerHTML='<div class=err>'+s.error+'</div>';return}
  const p=pivot(s.columns||[],s.rows||[]);
  if(!p||!p.data[0].length){box.innerHTML='<div class=empty>nothing in this window</div>';return}
  const opts={
    width:box.clientWidth||900,height:(payload.panel.height||8)*26,
    scales:{x:{time:true}},
    axes:[{stroke:"#4A5568",grid:{stroke:"#F1F3F6"},ticks:{stroke:"#E5E7EB"}},
          {stroke:"#4A5568",grid:{stroke:"#F1F3F6"},ticks:{stroke:"#E5E7EB"}}],
    series:[{},...p.names.map((n,i)=>({
      label:n,stroke:COLORS[i%COLORS.length],width:1.4,
      // A limit is drawn as it behaves: flat until it is changed.
      ...(/limit|cap|ceiling/i.test(n)?{dash:[6,4],width:1}:{})
    }))],
  };
  box.innerHTML="";
  new uPlot(opts,p.data.map((c,i)=>i===0?c.map(t=>t/1000):c),box);
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
  note.textContent=drawn+" panels";
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
  main.querySelectorAll(".tile").forEach(t=>t.onclick=()=>{
    chosen=t.dataset.run; // Arriving from the other page with a tab already named.
if(location.hash){
  const want=location.hash.slice(1);
  const t=[...document.querySelectorAll(".tab")].find(x=>x.dataset.scope===want);
  if(t){ document.querySelectorAll(".tab").forEach(x=>x.classList.toggle("on",x===t));
         scope=want; }
}
boot();
  });
}

async function boot(){
  // The filter group collapses whole, so the row cannot half-empty and reflow.
  const picking = scope==="runs" && !chosen;
  document.getElementById("filters").style.display=picking?"flex":"none";
  document.getElementById("range").style.display=scope==="window"?"":"none";
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
  if(back) back.onclick=()=>{chosen="";boot()};
  await load();
}
document.getElementById("refresh").onclick=load;
document.getElementById("range").onchange=load;
document.querySelectorAll(".tab").forEach(t=>t.onclick=async ()=>{
  document.querySelectorAll(".tab").forEach(x=>x.classList.toggle("on",x===t));
  scope=t.dataset.scope; chosen=""; await boot();
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
boot();
</script>` }
