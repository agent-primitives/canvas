package canvas

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func scopeTestPointer(p string) *EditSection { return &EditSection{Kind: "json-pointer", Pointer: &p} }
func scopeTestHeading(path ...string) *EditSection {
	return &EditSection{Kind: "markdown-heading", HeadingPath: path, Occurrence: 1}
}

// Catch semantic escapes, missing targets, and lossy numeric comparisons.
func TestEditScopeJSONPointer(t *testing.T) {
	for _, tc := range []struct {
		name, before, after, pointer string
		bad                          bool
	}{
		{"subtree type", "{\"a\":{\"x\":1},\"k\":[true,null]}", "{\"k\":[true,null],\"a\":\"changed\"}", "/a", false},
		{"escapes", "{\"a/b\":{\"~key\":1},\"k\":2}", "{\"a/b\":{\"~key\":[3]},\"k\":2}", "/a~1b/~0key", false},
		{"escape exactly once", "{\"~1\":1,\"/\":2}", "{\"~1\":3,\"/\":2}", "/~01", false},
		{"empty key", "{\"\":1,\"k\":2}", "{\"\":null,\"k\":2}", "/", false},
		{"root", "{\"a\":1}", "[null,2]", "", false},
		{"array node", "{\"a\":[1,{\"x\":2},3]}", "{\"a\":[1,false,3]}", "/a/1", false},
		{"large number", "{\"a\":0,\"n\":900719925474099312345678901}", "{\"n\":900719925474099312345678901,\"a\":1}", "/a", false},
		{"large exponent", "{\"a\":0,\"n\":1e9999}", "{\"a\":1,\"n\":1e9999}", "/a", false},
		{"rounded large number", "{\"a\":0,\"n\":9007199254740993}", "{\"a\":1,\"n\":9007199254740992}", "/a", true},
		{"exponent lexeme", "{\"a\":0,\"n\":1e0}", "{\"a\":1,\"n\":1}", "/a", true},
		{"decimal lexeme", "{\"a\":0,\"n\":1.0}", "{\"a\":1,\"n\":1}", "/a", true},
		{"negative zero lexeme", "{\"a\":0,\"n\":-0}", "{\"a\":1,\"n\":0}", "/a", true},
		{"add sibling", "{\"a\":1}", "{\"a\":2,\"b\":3}", "/a", true},
		{"remove sibling", "{\"a\":1,\"b\":3}", "{\"a\":2}", "/a", true},
		{"ancestor type", "{\"a\":{\"b\":1}}", "{\"a\":[2]}", "/a/b", true},
		{"remove target", "{\"a\":1}", "{}", "/a", true},
		{"append sibling", "[1,2]", "[3,2,4]", "/0", true},
		{"array sibling", "[1,2]", "[3,4]", "/0", true},
		{"duplicate output", "{\"a\":1}", "{\"a\":2,\"a\":3}", "/a", true},
		{"malformed output", "{\"a\":1}", "{\"a\":", "/a", true},
		{"multiple output", "{\"a\":1}", "{\"a\":2} {}", "/a", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope, err := resolveEditScope(tc.before, scopeTestPointer(tc.pointer))
			if err != nil {
				t.Fatal(err)
			}
			err = validateEditScope(tc.before, tc.after, scope)
			if (err != nil) != tc.bad {
				t.Fatalf("validation=%v, want error=%v", err, tc.bad)
			}
			if err != nil && !errors.Is(err, ErrEditScope) {
				t.Fatalf("unstable error: %v", err)
			}
		})
	}
}

func TestEditScopeInvalidSelector(t *testing.T) {
	p := "/a"
	for _, tc := range []struct {
		name, content string
		section       *EditSection
	}{
		{"missing", "{}", scopeTestPointer("/a")},
		{"missing nested", "{\"a\":null}", scopeTestPointer("/a/b")},
		{"bad escape", "{\"a~2\":1}", scopeTestPointer("/a~2")},
		{"trailing escape", "{\"a~\":1}", scopeTestPointer("/a~")},
		{"relative pointer", "{\"a\":1}", scopeTestPointer("a")},
		{"leading zero", "[1,2]", scopeTestPointer("/01")},
		{"negative index", "[1,2]", scopeTestPointer("/-1")},
		{"append index", "[1,2]", scopeTestPointer("/-")},
		{"plus index", "[1,2]", scopeTestPointer("/+1")},
		{"overflow index", "[1]", scopeTestPointer("/999999999999999999999")},
		{"absent index", "[1]", scopeTestPointer("/1")},
		{"duplicate key", "{\"a\":1,\"a\":2}", scopeTestPointer("/a")},
		{"duplicate nested", "{\"a\":{\"x\":1,\"x\":2}}", scopeTestPointer("/a")},
		{"duplicate escaped", "{\"a\":1,\"\\u0061\":2}", scopeTestPointer("/a")},
		{"malformed", "{\"a\":}", scopeTestPointer("/a")},
		{"unknown kind", "text", &EditSection{Kind: "other"}},
		{"missing pointer", "{\"a\":1}", &EditSection{Kind: "json-pointer"}},
		{"mixed path", "{\"a\":1}", &EditSection{Kind: "json-pointer", Pointer: &p, HeadingPath: []string{"A"}}},
		{"mixed occurrence", "{\"a\":1}", &EditSection{Kind: "json-pointer", Pointer: &p, Occurrence: 1}},
		{"long pointer", "{}", scopeTestPointer("/" + strings.Repeat("a", 512))},
		{"invalid pointer UTF8", "{}", scopeTestPointer("/" + string([]byte{255}))},
		{"missing heading path", "# A\n", &EditSection{Kind: "markdown-heading", Occurrence: 1}},
		{"missing occurrence", "# A\n", &EditSection{Kind: "markdown-heading", HeadingPath: []string{"A"}}},
		{"negative occurrence", "# A\n", &EditSection{Kind: "markdown-heading", HeadingPath: []string{"A"}, Occurrence: -1}},
		{"mixed pointer", "# A\n", &EditSection{Kind: "markdown-heading", Pointer: &p, HeadingPath: []string{"A"}, Occurrence: 1}},
		{"long heading", "# A\n", scopeTestHeading(strings.Repeat("a", 129))},
		{"invalid heading UTF8", "# A\n", scopeTestHeading(string([]byte{255}))},
		{"deep path", "# A\n", &EditSection{Kind: "markdown-heading", HeadingPath: make([]string, 17), Occurrence: 1}},
		{"absent heading", "# A\n", scopeTestHeading("B")},
		{"wrong full path", "# A\n## B\n", scopeTestHeading("B")},
		{"absent occurrence", "# A\n", &EditSection{Kind: "markdown-heading", HeadingPath: []string{"A"}, Occurrence: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := resolveEditScope(tc.content, tc.section); !errors.Is(err, ErrEditScope) {
				t.Fatalf("got %v, want ErrEditScope", err)
			}
		})
	}
}

func TestEditScopeMarkdownHeading(t *testing.T) {
	ticks := strings.Repeat(string(rune(96)), 3)
	for _, tc := range []struct {
		name, content, selected string
		section                 *EditSection
	}{
		{"children", "intro\n# A\nbody\n## Child\nchild\n# B\nkeep\n", "# A\nbody\n## Child\nchild\n", scopeTestHeading("A")},
		{"nested", "# A\n## B\none\n### C\ntwo\n## D\nkeep\n", "## B\none\n### C\ntwo\n", scopeTestHeading("A", "B")},
		{"skipped levels", "# A\n### B\none\n## C\n", "### B\none\n", scopeTestHeading("A", "B")},
		{"closing hashes", "   ## A ### \nbody\n## B\n", "   ## A ### \nbody\n", scopeTestHeading("A")},
		{"literal hashes", "# A#\nbody", "# A#\nbody", scopeTestHeading("A#")},
		{"empty title", "# A\n##\nbody\n# B\n", "##\nbody\n", scopeTestHeading("A", "")},
		{"CRLF", "intro\r\n# A\r\nbody\r\n## Child\r\nx\r\n# B\r\nkeep\r\n", "# A\r\nbody\r\n## Child\r\nx\r\n", scopeTestHeading("A")},
		{"occurrence", "# A\none\n# A\ntwo\n# B\n", "# A\ntwo\n", &EditSection{Kind: "markdown-heading", HeadingPath: []string{"A"}, Occurrence: 2}},
		{"nested occurrence", "# A\n## B\none\n# A\n## B\ntwo\n", "## B\ntwo\n", &EditSection{Kind: "markdown-heading", HeadingPath: []string{"A", "B"}, Occurrence: 2}},
		{"fences", "# A\n~~~ go\n# fake\n~~~\n" + ticks + "x\n# fake\n" + ticks + "\n# B\n", "# A\n~~~ go\n# fake\n~~~\n" + ticks + "x\n# fake\n" + ticks + "\n", scopeTestHeading("A")},
		{"long fence", "# A\n" + ticks + "x\n# fake\n" + ticks + "\n# B\n", "# A\n" + ticks + "x\n# fake\n" + ticks + "\n", scopeTestHeading("A")},
		{"lookalikes", "# A\n    # not heading\n\t# not heading\n#NoSpace\n####### not heading\n# B\n", "# A\n    # not heading\n\t# not heading\n#NoSpace\n####### not heading\n", scopeTestHeading("A")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope, err := resolveEditScope(tc.content, tc.section)
			if err != nil {
				t.Fatal(err)
			}
			start := strings.Index(tc.content, tc.selected)
			if scope.Start != start || scope.End != start+len(tc.selected) {
				t.Fatalf("range [%d,%d), want [%d,%d)", scope.Start, scope.End, start, start+len(tc.selected))
			}
			sum := sha256.Sum256([]byte(tc.selected))
			if scope.Hash != hex.EncodeToString(sum[:]) {
				t.Fatal("hash is not selected exact bytes")
			}
			after := tc.content[:scope.Start] + tc.selected + "new body\n" + tc.content[scope.End:]
			if err := validateEditScope(tc.content, after, scope); err != nil {
				t.Fatalf("valid replacement: %v", err)
			}
			// Only frozen outside bytes constrain Markdown output; selected text may change.
			for _, escaped := range []string{"prefix" + after, after + "suffix", strings.Replace(after, "# B", "# Changed", 1)} {
				if escaped == after {
					continue
				}
				// Renaming a heading inside the selected byte range is allowed.
				if at := strings.Index(after, "# B"); at >= scope.Start && at < scope.Start+len(tc.selected)+len("new body\n") && escaped == strings.Replace(after, "# B", "# Changed", 1) {
					continue
				}
				// A suffix is in scope when the selected section reaches EOF.
				if escaped == after+"suffix" && scope.End == len(tc.content) {
					continue
				}
				// A prefix is in scope when the section starts at byte zero.
				if escaped == "prefix"+after && scope.Start == 0 {
					continue
				}
				if err := validateEditScope(tc.content, escaped, scope); !errors.Is(err, ErrEditScope) {
					t.Fatalf("outside edit accepted: %q", escaped)
				}
			}
			tc.section.HeadingPath[0] = "mutated"
			if scope.HeadingPath[0] == "mutated" {
				t.Fatal("scope aliases request path")
			}
		})
	}
}

func TestEditScopeMarkdownUnsupported(t *testing.T) {
	ticks := strings.Repeat(string(rune(96)), 3)
	for _, content := range []string{
		"# A\nTitle\n-----\n# B\n", "Title\n=====\n# A\n",
		"# A\n~~~\n# B\n", ticks + "\n# A\n", "# A\n" + ticks + "go" + string(rune(96)) + "bad\n# B\n",
	} {
		if _, err := resolveEditScope(content, scopeTestHeading("A")); !errors.Is(err, ErrEditScope) {
			t.Fatalf("unsupported markdown accepted: %q (%v)", content, err)
		}
	}
	if _, err := resolveEditScope("# A\n~~~\nTitle\n====\n~~~\n# B\n", scopeTestHeading("A")); err != nil {
		t.Fatal(err)
	}
}

func TestEditScopeWholeAndBounds(t *testing.T) {
	for _, content := range []string{"", "plain text", strings.Repeat("a", 65536), "# A\n~~~\nunclosed"} {
		scope, err := resolveEditScope(content, nil)
		if err != nil {
			t.Fatal(err)
		}
		if scope.Kind != "whole" || scope.Start != 0 || scope.End != len(content) {
			t.Fatalf("wrong whole scope: %+v", scope)
		}
		sum := sha256.Sum256([]byte(content))
		if scope.Hash != hex.EncodeToString(sum[:]) {
			t.Fatal("whole hash is not exact bytes")
		}
		if err := validateEditScope(content, "replacement", scope); err != nil {
			t.Fatal(err)
		}
	}
	for _, bad := range []string{string([]byte{255}), strings.Repeat("x", 65537), "null\x00", "Bearer secret", "PRIVATE KEY"} {
		if _, err := resolveEditScope(bad, nil); !errors.Is(err, ErrEditScope) {
			t.Fatalf("invalid base accepted (%v)", err)
		}
		scope, err := resolveEditScope("valid", nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateEditScope("valid", bad, scope); !errors.Is(err, ErrEditScope) {
			t.Fatal("invalid candidate accepted")
		}
	}
}

func TestEditScopeFrozenEvidence(t *testing.T) {
	for _, tc := range []struct {
		content string
		section *EditSection
	}{
		{"{\"a\":{\"z\":1,\"b\":2},\"keep\":3}", scopeTestPointer("/a")},
		{"intro\n# A\nbody\n# B\nkeep\n", scopeTestHeading("A")},
		{"whole document", nil},
	} {
		scope, err := resolveEditScope(tc.content, tc.section)
		if err != nil {
			t.Fatal(err)
		}
		for _, mutate := range []func(*EditScope){
			func(s *EditScope) { s.Hash = "forged" }, func(s *EditScope) { s.Start++ },
			func(s *EditScope) { s.End++ }, func(s *EditScope) { s.Kind = "unknown" },
			func(s *EditScope) { s.Pointer = "/other" }, func(s *EditScope) { s.HeadingPath = []string{"other"} },
			func(s *EditScope) { s.Occurrence++ },
		} {
			forged := scope
			mutate(&forged)
			if err := validateEditScope(tc.content, tc.content, forged); !errors.Is(err, ErrEditScope) {
				t.Fatalf("forged scope accepted: %+v", forged)
			}
		}
	}
	first, err := resolveEditScope("{\"a\":{\"z\":1e0,\"b\":2}}", scopeTestPointer("/a"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolveEditScope("{ \"a\": {\"b\":2,\"z\":1e0} }", scopeTestPointer("/a"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("{\"b\":2,\"z\":1e0}"))
	if first.Hash != second.Hash || first.Hash != hex.EncodeToString(sum[:]) {
		t.Fatal("JSON hash is not canonical with number lexemes")
	}
	if err := validateEditScope("{\"a\":{\"z\":1,\"b\":2}}", "{\"a\":null}", first); !errors.Is(err, ErrEditScope) {
		t.Fatal("original target drift accepted")
	}
}

func TestEditScopeJSONTags(t *testing.T) {
	raw, err := json.Marshal(scopeTestPointer(""))
	if err != nil || string(raw) != "{\"kind\":\"json-pointer\",\"pointer\":\"\"}" {
		t.Fatalf("explicit root lost: %s (%v)", raw, err)
	}
	var scope EditScope
	if err := json.Unmarshal([]byte("{\"kind\":\"markdown-heading\",\"headingPath\":[\"A\"],\"occurrence\":1,\"start\":2,\"end\":8,\"hash\":\"hash\"}"), &scope); err != nil {
		t.Fatal(err)
	}
	if scope.Kind != "markdown-heading" || len(scope.HeadingPath) != 1 || scope.Occurrence != 1 || scope.Start != 2 || scope.End != 8 || scope.Hash != "hash" {
		t.Fatalf("scope JSON fields lost: %+v", scope)
	}
}
