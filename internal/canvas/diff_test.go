package canvas

import "testing"

func TestDocumentDiffHunks(t *testing.T) {
	const head = "--- f (proposal base)\n+++ f (proposed)\n"
	for name, tc := range map[string]struct{ before, after, want string }{
		"same":           {"a\n", "a\n", "No content changes.\n"},
		"replace":        {"a\nb\nc\n", "a\nx\nc\n", head + "@@ -2,1 +2,1 @@\n-b\n+x\n"},
		"append":         {"a\nb\n", "a\nb\nc\n", head + "@@ -2,0 +3,1 @@\n+c\n"},
		"prepend":        {"b\n", "a\nb\n", head + "@@ -0,0 +1,1 @@\n+a\n"},
		"delete":         {"a\nb\nc\n", "a\nc\n", head + "@@ -2,1 +1,0 @@\n-b\n"},
		"from empty":     {"", "a\n", head + "@@ -0,0 +1,1 @@\n+a\n"},
		"to empty":       {"a\n", "", head + "@@ -1,1 +0,0 @@\n-a\n"},
		"add newline":    {"a\nb", "a\nb\n", head + "@@ -2,1 +2,1 @@\n-b\n\\ No newline at end of file\n+b\n"},
		"remove newline": {"a\nb\n", "a\nb", head + "@@ -2,1 +2,1 @@\n-b\n+b\n\\ No newline at end of file\n"},
		"blank line":     {"a\n", "a\n\n", head + "@@ -1,0 +2,1 @@\n+\n"},
	} {
		if got := documentDiff("f", tc.before, tc.after); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", name, got, tc.want)
		}
	}
}
