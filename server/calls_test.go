package main

import "testing"

// A shell write is recorded as the file it wrote. Run 61eca0ae (APF-3313)
// wrote `Already reported: APF-3286` into its bug draft with the first command
// below; the record said `"$F"`, and the data trace could not find the run's
// own write.
func TestBashWriteTargetIsTheFileWritten(t *testing.T) {
	cases := []struct{ cmd, want string }{
		{"cd /runs/APF-3313/artifacts/bugs; F=ProductBug-x.md\ncat >> \"$F\" <<'EOF'\nAlready reported: APF-3286\nEOF",
			"/runs/APF-3313/artifacts/bugs/ProductBug-x.md"},
		{"cd /runs/APF-3313/artifacts/bugs; cat >> ProductBug-x.md <<'EOF'\nx\nEOF",
			"/runs/APF-3313/artifacts/bugs/ProductBug-x.md"},
		{"cd /runs/T && echo hi > out/a.txt", "/runs/T/out/a.txt"},
		{"D=/runs/T; echo hi > \"${D}/b.txt\"", "/runs/T/b.txt"},
		{"cat > /runs/T/req-5/agent_output.txt <<'EOF'\nx\nEOF", "/runs/T/req-5/agent_output.txt"},
		{"echo hi | tee -a /runs/T/log.txt", "/runs/T/log.txt"},
		// What cannot be worked out stays as it was written.
		{"echo hi > \"$UNSET\"", "\"$UNSET\""},
		{"echo hi > rel.txt", "rel.txt"},
		{"cd \"$HOME\"; echo hi > rel.txt", "rel.txt"},
		// --into names are the engine tool's to resolve, not the shell's.
		{"cd /runs/T/req-7; python3 /engine/tools/put_rows.py --into rows.json <<'EOF'\n[]\nEOF", "rows.json"},
	}
	for _, c := range cases {
		kind, target, _ := classify("Bash", map[string]any{"command": c.cmd})
		if kind != "write" || target != c.want {
			t.Errorf("%q: got %s %q, want write %q", c.cmd, kind, target, c.want)
		}
	}
}
