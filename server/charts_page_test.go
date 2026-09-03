package main

import (
	"strings"
	"testing"
)

// The rule: a span of time on an axis is spelled out. The gate is
// axisTimeIsSpelledOut; this drives it green on the page as served and red
// on a formatter that abbreviates, because a gate nobody has watched refuse
// is not a gate.

func TestAxisTimeIsSpelledOutOnThePage(t *testing.T) {
	if err := axisTimeIsSpelledOut(chartsPageHTML("runs")); err != nil {
		t.Fatal(err)
	}
}

func TestAxisTimeGateRefusesAnAbbreviation(t *testing.T) {
	page := chartsPageHTML("runs")
	start := strings.Index(page, "function dur(")
	// The same formatter with one label clipped to a letter.
	bad := page[:start] + "function dur(v){ return TIME_WORDS.hours && Math.round(v/3600)+\"h\"; }\n}" + page[start:]
	if err := axisTimeIsSpelledOut(bad); err == nil {
		t.Fatal("a formatter writing \"h\" passed the gate")
	}
	// And one that ignores the words altogether.
	noWords := page[:start] + "function dur(v){ return Math.round(v/60)+\" minutes\"; }\n}" + page[start:]
	if err := axisTimeIsSpelledOut(noWords); err == nil {
		t.Fatal("a formatter that does not read TIME_WORDS passed the gate")
	}
}

func TestTimeWordsGateRefusesAClippedWord(t *testing.T) {
	saved := timeWords["hours"]
	timeWords["hours"] = "hrs"
	defer func() { timeWords["hours"] = saved }()
	if err := axisTimeIsSpelledOut(chartsPageHTML("runs")); err == nil {
		t.Fatal("timeWords with \"hrs\" passed the gate")
	}
}
