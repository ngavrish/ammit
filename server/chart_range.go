package main

import (
	"fmt"
	"regexp"
	"strings"
)

// The gate behind `a-chart-starts-where-its-data-does`.
//
// The rule was written after one page drew a forty-minute run across twelve
// hours of nothing, four separate ways in one day, and its answer was that the
// range is decided once, on the way in. That decision is now in the page - the
// x scale clips the data to the window, and a reference line rides its own
// scale so a sixty-dollar ceiling cannot flatten an eight-dollar run - and
// until now nothing held it there. Any renderer added later could read the
// rows its own way, any edit could widen the range back to the window, and the
// only thing noticing would be a person looking at a blank left-hand edge.
//
// So it is checked structurally, on the page as served, which is the same
// string `axisTimeIsSpelledOut` reads:
//
//   - one place decides the time range, and it clips the data to the window
//     rather than taking either alone;
//   - a reference line sits on a scale of its own, not on the value axis;
//   - every renderer that draws against time reads its rows through the one
//     function, so a new chart kind cannot be exempt by having been written
//     after the rule.
//
// Counted rather than listed: the renderers are found on the page, so adding
// one adds it to the check.

var (
	drawFn   = regexp.MustCompile(`function (draw[A-Z]\w*)\(`)
	xRange   = regexp.MustCompile(`x:\{time:true,\s*range:`)
	limScale = regexp.MustCompile(`lim:\{from:"y",\s*range:`)
	// What makes a renderer answerable here is an axis against time, not a
	// timestamp formatted in a cell: `secs(` was in the first version and it
	// caught the table and the stat tile, neither of which has an axis at all.
	timeAxis  = regexp.MustCompile(`payload\.from|payload\.to|time:true|windowMs\(`)
	rowReader = "rowsOf("
)

func chartStartsWhereItsDataDoes(page string) error {
	// One place, not one per chart kind. Two would be two answers to the same
	// question and the second would drift.
	if n := len(xRange.FindAllString(page, -1)); n != 1 {
		return fmt.Errorf("the page decides its time range in %d places; the "+
			"rule is one place, on the way in", n)
	}
	body := rangeBody(page)
	if body == "" {
		return fmt.Errorf("the time range function has no body to read")
	}
	// Clipped, not either one alone: the window alone drew a short run as a
	// hair at one edge, the data alone let a run that merely overlaps the
	// window draw all of itself.
	for _, want := range []struct{ frag, why string }{
		{"Math.max(", "the range must start at the later of the data and the window"},
		{"Math.min(", "the range must end at the earlier of the data and the window"},
	} {
		if !strings.Contains(body, want.frag) {
			return fmt.Errorf("the time range does not clip: %s", want.why)
		}
	}
	// A limit, a cap, a threshold is drawn where it falls and does not stretch
	// the axis it is drawn against.
	if !limScale.MatchString(page) {
		return fmt.Errorf("a reference line shares the value axis - it must " +
			"ride a scale of its own or it stretches the chart to reach it")
	}
	// And every renderer against time reads its rows through the one function.
	var stray []string
	for _, m := range drawFn.FindAllStringSubmatchIndex(page, -1) {
		name := page[m[2]:m[3]]
		fn := functionBody(page, m[0])
		if !timeAxis.MatchString(fn) {
			continue // a table, a tile, a pie: no axis to start anywhere
		}
		if !strings.Contains(fn, rowReader) {
			stray = append(stray, name)
		}
	}
	if len(stray) > 0 {
		return fmt.Errorf("%s draws against time and reads its rows its own "+
			"way - a chart kind the rule does not reach is a chart kind that "+
			"starts where the window does", strings.Join(stray, ", "))
	}
	return nil
}

// rangeBody is the text of the one x-range function, from its match to the end
// of its block.
func rangeBody(page string) string {
	at := xRange.FindStringIndex(page)
	if at == nil {
		return ""
	}
	end := strings.Index(page[at[1]:], "}}")
	if end < 0 {
		return ""
	}
	return page[at[1] : at[1]+end]
}

// functionBody is a `function name(...)` block, read by counting braces so a
// nested one does not end it early.
func functionBody(page string, at int) string {
	open := strings.Index(page[at:], "{")
	if open < 0 {
		return ""
	}
	depth, i := 0, at+open
	for ; i < len(page); i++ {
		switch page[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return page[at : i+1]
			}
		}
	}
	return page[at:]
}
