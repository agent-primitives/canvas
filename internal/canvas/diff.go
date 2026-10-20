package canvas

import (
	"fmt"
	"strings"
)

const noNewline = "\\ No newline at end of file\n"

// documentDiff shows a change as one unified diff hunk without context lines:
// lines shared at the start and end are trimmed and the rest is replaced. It is
// for review only; nothing here is ever applied as a patch.
func documentDiff(path, before, after string) string {
	if before == after {
		return "No content changes.\n"
	}
	a, b := diffLines(before), diffLines(after)
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	endA, endB := len(a), len(b)
	for endA > prefix && endB > prefix && a[endA-1] == b[endB-1] {
		endA--
		endB--
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s (proposal base)\n+++ %s (proposed)\n@@ -%s +%s @@\n", path, path, hunkRange(prefix, endA-prefix), hunkRange(prefix, endB-prefix))
	for _, line := range a[prefix:endA] {
		writeDiffLine(&out, "-", line)
	}
	for _, line := range b[prefix:endB] {
		writeDiffLine(&out, "+", line)
	}
	return out.String()
}

// diffLines splits text into lines. A last line with no trailing newline keeps
// a "\x00" marker so it differs from the same line with a newline.
func diffLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if !strings.HasSuffix(text, "\n") {
		lines[len(lines)-1] += "\x00"
	}
	return lines
}

func writeDiffLine(out *strings.Builder, sign, line string) {
	if text, ok := strings.CutSuffix(line, "\x00"); ok {
		out.WriteString(sign + text + "\n" + noNewline)
		return
	}
	out.WriteString(sign + line + "\n")
}

// hunkRange formats one side of a hunk header. An empty range names the line
// before it, so an append after line 2 is "2,0".
func hunkRange(prefix, count int) string {
	if count == 0 {
		return fmt.Sprintf("%d,0", prefix)
	}
	return fmt.Sprintf("%d,%d", prefix+1, count)
}
