package main

// The shell, written once.
//
// It used to be a bar across the top with the tabs on the right, and the bar
// existed twice - typed into both pages - and drifted every time either was
// touched. Now it is a sidebar down the left, in one place, and the pages
// differ only in which entry is lit and what they put in the strip at the top.
//
// The shape is the one dashboards settled on: navigation in a narrow column
// that can fold to its icons, the page's own controls in a strip along the top,
// and the content as cards on a dark ground. The colours are the house's - deep
// space blue for the ground, bronze for the one thing that is active, data grey
// for everything that is said quietly.
func headerHTML(active string) string {
	tab := func(href, label, name string) string {
		on := ""
		if name == active {
			on = " on"
		}
		scope := ""
		if name != "limits" {
			scope = ` data-scope=` + name
		}
		return `<a class="tab` + on + `"` + scope + ` href="` + href + `" title="` + label + `">` +
			`<i class=ico>` + icon(name) + `</i><span>` + label + `</span></a>`
	}
	return `
<aside id=side>
  <a class=brand href="/ammit/limits" aria-label="ammit">
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
    <span class=brand-text><b>AMMIT</b><small>Chiron.consulting</small></span>
  </a>

  <nav id=tabs>
    <span id=glow aria-hidden=true></span>` +
		tab("/ammit/limits", "Limits", "limits") + pageTabs(tab) + `
  </nav>

  <div class=side-foot>
    <a class=home href="https://dokimos.chiron.systems" title="back to dokimos"><i class=ico>` + icon("back") + `</i><span>dokimos</span></a>
    <button id=fold title="fold the sidebar" aria-label="fold the sidebar"><i class=ico>` + icon("fold") + `</i></button>
  </div>
</aside>`
}

// One small stroked glyph per entry, so the folded sidebar still says what
// each entry is. Drawn on a 20-grid, no fill: a mark, not a picture.
func icon(name string) string {
	const open = `<svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">`
	body := map[string]string{
		"limits":   `<path d="M4 6h12M4 10h12M4 14h12"/><circle cx="8" cy="6" r="1.6" fill="currentColor" stroke="none"/><circle cx="13" cy="10" r="1.6" fill="currentColor" stroke="none"/><circle cx="7" cy="14" r="1.6" fill="currentColor" stroke="none"/>`,
		"runs":     `<rect x="3.5" y="3.5" width="13" height="13" rx="2.5"/><path d="M8 7.5l4 2.5-4 2.5z" fill="currentColor" stroke="none"/>`,
		"window":   `<circle cx="10" cy="10" r="6.5"/><path d="M10 6.5V10l2.5 1.8"/>`,
		"lifetime": `<path d="M6 10c0-2 1.3-3.2 2.8-3.2 1.7 0 2.4 1.6 2.4 3.2s.7 3.2 2.4 3.2C15.1 13.2 16 12 16 10s-.9-3.2-2.4-3.2c-1.7 0-2.4 1.6-2.4 3.2s-.7 3.2-2.4 3.2C7.3 13.2 6 12 6 10z"/>`,
		"heal":     `<path d="M3 10h3l2-4 3 8 2-4h4"/>`,
		"model":    `<circle cx="5" cy="10" r="1.8"/><circle cx="15" cy="5" r="1.8"/><circle cx="15" cy="15" r="1.8"/><path d="M6.7 9.2 13.3 5.8M6.7 10.8l6.6 3.4"/>`,
		"back":     `<path d="M11 5l-5 5 5 5M6 10h10"/>`,
		"fold":     `<path d="M12 5l-5 5 5 5"/>`,
	}[name]
	return open + body + `</svg>`
}

// The rules for the shell, in the same one place. Both pages inherit them and
// state nothing of their own about the ground, the type or the sidebar.
const headerCSS = `
:root{
  --bg:#02101F; --navy:#001F3F; --raised:#062A4F; --deeper:#010B17;
  --hair:rgba(247,250,252,.08); --hair-strong:rgba(247,250,252,.16); --hair-soft:rgba(247,250,252,.045);
  --ink:#F7FAFC; --mute:#A0AEC0; --dim:#718096; --slate:#A0AEC0;
  --bronze:#CD7F32; --bronze-hi:#E39A4C; --bronze-wash:rgba(205,127,50,.12); --bronze-glow:rgba(205,127,50,.45);
  --sky:#38BDF8; --good:#34D399; --bad:#F87171; --warm:#FBBF24;
  --mono:"JetBrains Mono",ui-monospace,SFMono-Regular,Menlo,monospace;
  --sans:"Plus Jakarta Sans","Inter",ui-sans-serif,system-ui,-apple-system,sans-serif;
  --side:224px; --side-min:64px; --top:56px;
  --ease:cubic-bezier(.2,.7,.2,1); --quick:.18s;
  --arrow:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 13 9'%3E%3Cpath d='M4.8.7 1 4.5l3.8 3.8M1 4.5h11' fill='none' stroke='%23000' stroke-width='1.6' stroke-linecap='round' stroke-linejoin='round'/%3E%3C/svg%3E");
  color-scheme:dark;
}
html{background:var(--bg)}
body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.55 var(--sans);
  display:grid;grid-template-columns:var(--side) minmax(0,1fr);min-height:100vh;
  transition:grid-template-columns .28s var(--ease)}
body.folded{grid-template-columns:var(--side-min) minmax(0,1fr)}
body::before{content:"";position:fixed;inset:0;pointer-events:none;z-index:0;
  background:
    radial-gradient(1100px 600px at 85% -10%, rgba(205,127,50,.07), transparent 60%),
    radial-gradient(900px 700px at -10% 110%, rgba(56,189,248,.05), transparent 60%)}
.page{position:relative;z-index:1;min-width:0;display:flex;flex-direction:column;min-height:100vh}

/* The sidebar: the house colour, one column, sticky for the whole height. */
#side{position:sticky;top:0;height:100vh;z-index:40;display:flex;flex-direction:column;
  background:linear-gradient(180deg,var(--navy),#00172F);border-right:1px solid var(--hair);
  padding:14px 10px 12px;gap:10px;overflow:hidden}
.brand{display:flex;align-items:center;gap:10px;padding:6px 8px 12px;text-decoration:none;
  border-bottom:1px solid var(--hair);margin-bottom:4px;white-space:nowrap}
.brand svg{display:block;flex:none}
.brand-text{display:flex;flex-direction:column;transition:opacity var(--quick),transform .28s var(--ease)}
.brand b{display:block;font:800 18px/1 var(--sans);letter-spacing:-.01em;color:var(--ink)}
.brand small{display:block;font:600 7px/1 var(--mono);letter-spacing:.32em;color:var(--mute);
  text-transform:uppercase;margin-top:3px}
.folded .brand-text{opacity:0;transform:translateX(-8px)}

#tabs{position:relative;display:flex;flex-direction:column;gap:2px;padding:4px 0}
.tab{position:relative;display:flex;align-items:center;gap:11px;height:38px;padding:0 10px;
  border-radius:8px;color:var(--mute);text-decoration:none;white-space:nowrap;
  font:500 13.5px/1 var(--mono);letter-spacing:0;
  transition:color var(--quick),background var(--quick),transform var(--quick)}
.tab .ico{flex:none;width:20px;height:20px;display:grid;place-items:center;opacity:.85;
  transition:opacity var(--quick),transform .25s var(--ease)}
.tab .ico svg{width:18px;height:18px;display:block}
.tab span{display:inline-block;white-space:nowrap;overflow:hidden;max-width:160px;
  transition:opacity var(--quick),transform .28s var(--ease),max-width .28s var(--ease)}
.tab:hover{color:var(--ink);background:var(--hair-soft)}
.tab:hover .ico{transform:translateX(1px)}
.tab:active{transform:scale(.985)}
.tab.on{color:var(--ink);background:var(--bronze-wash);font-weight:700}
.tab.on .ico{color:var(--bronze-hi);opacity:1}
.folded .tab span{opacity:0;transform:translateX(-6px);max-width:0}
.folded .tab{justify-content:center;padding:0;gap:0}
.folded .tab .ico{width:22px}
/* The bar of bronze that says where you are. One element, moved by the page
   rather than painted anew on each entry, so it is seen travelling. */
#glow{position:absolute;left:-10px;width:3px;height:22px;border-radius:0 3px 3px 0;
  background:var(--bronze);box-shadow:0 0 12px var(--bronze-glow);opacity:0;
  transition:top .32s var(--ease),opacity var(--quick)}
#glow.set{opacity:1}

.side-foot{margin-top:auto;display:flex;align-items:center;gap:6px;padding-top:10px;
  border-top:1px solid var(--hair)}
.home{flex:1;display:flex;align-items:center;gap:10px;height:34px;padding:0 10px;border-radius:8px;
  color:var(--mute);text-decoration:none;white-space:nowrap;overflow:hidden;
  font:500 12.5px/1 var(--mono);letter-spacing:.06em;text-transform:uppercase;
  transition:color var(--quick),background var(--quick)}
.home .ico{flex:none;width:20px;height:20px;display:grid;place-items:center}
.home .ico svg{width:18px;height:18px;display:block;transition:transform .25s var(--ease)}
.home:hover{color:var(--ink);background:var(--hair-soft)}
.home:hover .ico svg{transform:translateX(-3px)}
.home span{display:inline-block;overflow:hidden;max-width:120px;transition:opacity var(--quick),max-width .28s var(--ease)}
.folded .home span{opacity:0;max-width:0}
.folded .home{flex:none;width:0;padding:0;opacity:0}
#fold{flex:none;width:34px;height:34px;border:1px solid var(--hair);border-radius:8px;
  background:none;color:var(--mute);cursor:pointer;display:grid;place-items:center;padding:0;
  transition:color var(--quick),border-color var(--quick),background var(--quick)}
#fold .ico{width:20px;height:20px;display:grid;place-items:center}
#fold svg{width:18px;height:18px;display:block;transition:transform .32s var(--ease)}
#fold:hover{color:var(--ink);border-color:var(--hair-strong);background:var(--hair-soft)}
.folded #fold svg{transform:rotate(180deg)}
.folded .side-foot{justify-content:center}

/* The strip along the top of the page, where the page keeps its own controls. */
#bar{position:sticky;top:0;z-index:25;min-height:var(--top);padding:9px 18px;
  background:rgba(2,16,31,.78);backdrop-filter:saturate(140%) blur(12px);
  border-bottom:1px solid var(--hair);display:flex;align-items:center;gap:12px;flex-wrap:wrap}
#bar .title{display:flex;flex-direction:column;gap:2px;line-height:1;margin-right:4px;min-width:0}
#bar .title b{white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:22ch}
#bar .title b{font:700 15px/1 var(--sans);color:var(--ink);letter-spacing:-.01em}
#bar .title em{font:400 11px/1 var(--mono);font-style:normal;color:var(--dim)}

/* Motion is a courtesy, not a requirement. */
@media (prefers-reduced-motion:reduce){
  *,*::before,*::after{animation-duration:.001s!important;animation-iteration-count:1!important;transition-duration:.001s!important}
}
@media (max-width:820px){
  body{grid-template-columns:var(--side-min) minmax(0,1fr)}
  .brand-text,.tab span,.home span{display:none}
  .tab{justify-content:center;padding:0}
}
`

// What the shell does on its own: folds when asked and remembers it, and
// moves the bronze bar to whichever entry is lit. Pages that switch entries
// without a reload call shell.mark(name).
const headerJS = `
const shell=(()=>{
  const body=document.body, glow=document.getElementById("glow");
  const key="ammit.side";
  try{ if(localStorage.getItem(key)==="folded") body.classList.add("folded"); }catch(e){}
  function mark(name){
    const tabs=[...document.querySelectorAll("#tabs .tab")];
    let on=null;
    tabs.forEach(t=>{const hit=name!=null?(t.dataset.scope||"limits")===name:t.classList.contains("on");
      t.classList.toggle("on",hit); if(hit) on=t;});
    if(!on){ glow.classList.remove("set"); return; }
    glow.style.top=(on.offsetTop+(on.offsetHeight-22)/2)+"px";
    requestAnimationFrame(()=>glow.classList.add("set"));
  }
  document.getElementById("fold").onclick=()=>{
    body.classList.toggle("folded");
    try{ localStorage.setItem(key, body.classList.contains("folded")?"folded":"open"); }catch(e){}
    setTimeout(()=>mark(),300);
  };
  addEventListener("resize",()=>mark());
  mark();
  return {mark};
})();
`

// pageTabs is one tab per page in the registry, in its order.
func pageTabs(tab func(path, label, key string) string) string {
	out := ""
	for _, p := range pages {
		out += tab(p.Path, p.Tab, p.Key)
	}
	return out
}
