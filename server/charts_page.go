package main

import (
	_ "embed"
	"strings"
)

// The page. uPlot is vendored rather than fetched: a chart that needs the
// internet is a chart that is blank on the machine that most needs it.

//go:embed uPlot.iife.min.js
var uplotJS string

//go:embed uPlot.min.css
var uplotCSS string

// The page, in three files a person can open: what it looks like, what it
// does, and the skeleton that holds them. They were one Go string of fifteen
// hundred lines, and an editor could not tell the CSS from the JavaScript
// from the Go. The server still splices its own tables in - the limit titles,
// the pages, the time words - at the marks below, so the page as served is
// what the gate reads and what the browser runs.
//
//go:embed charts.html
var chartsHTML string

//go:embed charts.css
var chartsCSS string

//go:embed charts.js
var chartsJS string

func chartsPageHTML(active string) string {
	styles := strings.NewReplacer("/*@HEADER_CSS@*/", headerCSS, "/*@FOOTER_CSS@*/", footerCSS).Replace(chartsCSS)
	script := strings.NewReplacer("/*@LIMIT_TITLES@*/", limitTitlesJS(), "/*@PAGES@*/", pagesJSON(),
		"/*@TIME_WORDS@*/", timeWordsJS()).Replace(chartsJS)
	return strings.NewReplacer("/*@STYLES@*/", uplotCSS+styles, "/*@HEADER_HTML@*/", headerHTML(active), "/*@FOOTER_HTML@*/", footerHTML(),
		"/*@UPLOT@*/", uplotJS, "/*@SHELL@*/", headerJS, "/*@SCRIPT@*/", script).Replace(chartsHTML)
}
