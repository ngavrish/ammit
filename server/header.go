package main

import "strings"

// The bar, written once.
//
// It existed twice — the same markup and the same rules typed into two files —
// and the two drifted every time either was touched: the name at twenty on one
// page and fifteen on the other, the back button in capitals here and not there,
// a pixel of difference in the wordmark from a wrapper that was flex in one copy
// and not the other. Each of those was found by looking, fixed on one side, and
// then found again.
//
// Now there is one of it. What differs between the pages is which tab is lit.
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
		return `<a class="tab` + on + `"` + scope + ` href="` + href + `">` + label + `</a>`
	}
	return `
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
    <span class=brand-text><b>AMMIT</b><small>Chiron.consulting</small></span>
  </span>

  <nav id=tabs>` +
		tab("/ammit/limits", "Limits", "limits") + pageTabs(tab) + `
  </nav>
</header>`
}

// The rules for it, in the same one place.
//
// The bar states the base its children inherit — the family, the size, the
// colour — rather than taking it from the page. One of these pages is light and
// the other is not, and anything without a size or a colour of its own came out
// different on the two.
const headerCSS = `
header{position:sticky;top:0;z-index:30;padding:0 24px;height:81px;
  font:15px/1.65 "Plus Jakarta Sans","Inter",ui-sans-serif,system-ui,-apple-system,sans-serif;
  color:#F7FAFC;background:rgba(0,31,63,.9);
  backdrop-filter:saturate(140%) blur(10px);
  border-bottom:1px solid rgba(255,255,255,.08);
  display:grid;grid-template-columns:1fr auto 1fr;align-items:center;gap:16px}

.home{justify-self:start;flex:none;display:inline-flex;align-items:center;gap:9px;
  align-self:center;height:36px;padding:0 20px 0 16px;text-decoration:none;
  border:1px solid rgba(205,127,50,.35);border-radius:0;background:none;
  color:#A0AEC0;font:400 14px/20px "JetBrains Mono",ui-monospace,SFMono-Regular,Menlo,monospace;
  letter-spacing:.12em;text-transform:uppercase;
  transition:color .15s,border-color .15s}
.home::before{content:"";width:13px;height:9px;flex:none;background:#CD7F32;
  -webkit-mask:var(--arrow) center/contain no-repeat;
  mask:var(--arrow) center/contain no-repeat;transition:transform .15s}
.home:hover{color:#F7FAFC;border-color:#CD7F32}
.home:hover::before{transform:translateX(-3px)}

.brand{justify-self:center;display:flex;align-items:center;gap:9px}
.brand svg{display:block}
/* Block children rather than <b><br><small>: a bare break takes the browser's
   own leading, which inflates the line past its font-size. */
.brand-text{display:flex;flex-direction:column}
.brand b{display:block;font:800 20px/1 "Plus Jakarta Sans","Inter",ui-sans-serif,system-ui,sans-serif;
  letter-spacing:-.01em;color:#F7FAFC}
.brand small{display:block;
  font:600 7px/1 "JetBrains Mono",ui-monospace,SFMono-Regular,Menlo,monospace;
  letter-spacing:.32em;color:#A0AEC0;text-transform:uppercase;margin-top:3px}

/* The menu from dokimos.chiron.systems, measured off the live site. */
#tabs{justify-self:end;margin-left:auto;display:flex;align-items:center;gap:8px;flex:none}
.tab{border:0;background:none;border-radius:0;height:36px;padding:0 24px;
  color:#A0AEC0;
  font:400 14px/20px "JetBrains Mono",ui-monospace,SFMono-Regular,Menlo,monospace;
  letter-spacing:0;text-transform:none;cursor:pointer;text-decoration:none;
  display:inline-flex;align-items:center;transition:color .15s,background .15s}
.tab:hover{color:#F7FAFC}
.tab.on{background:#CD7F32;color:#001F3F;font-weight:700}

:root{--arrow:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 13 9'%3E%3Cpath d='M4.8.7 1 4.5l3.8 3.8M1 4.5h11' fill='none' stroke='%23000' stroke-width='1.6' stroke-linecap='round' stroke-linejoin='round'/%3E%3C/svg%3E");}
`

var _ = strings.TrimSpace

// pageTabs is one tab per page in the registry, in its order.
func pageTabs(tab func(path, label, key string) string) string {
	out := ""
	for _, p := range pages {
		out += tab(p.Path, p.Tab, p.Key)
	}
	return out
}
