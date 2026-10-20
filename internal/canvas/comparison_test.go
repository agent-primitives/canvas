package canvas

import (
	"strings"
	"testing"
)

func TestDocumentComparison(t *testing.T) {
	for _, tc := range []struct {
		name, path, before, after, kind, want string
	}{
		{"nested context", "f.json", `{"meta":{"title":"Before","kept":true},"tail":0}`, `{"meta":{"title":"After","kept":true},"tail":0}`, "json-lines", " {\n   \"meta\": {\n-    \"title\": \"Before\",\n+    \"title\": \"After\",\n     \"kept\": true\n   },\n"},
		{"array and type", "f.json", `{"items":[1,"two"],"flag":true}`, `{"items":[1,2],"flag":null}`, "json-lines", "-    \"two\"\n-  ],\n-  \"flag\": true\n+    2\n+  ],\n+  \"flag\": null\n"},
		{"large numbers", "f.json", `{"n":9007199254740992}`, `{"n":9007199254740993}`, "json-lines", "-  \"n\": 9007199254740992\n+  \"n\": 9007199254740993\n"},
		{"formatting only", "f.json", "{\"n\":1}\n", " { \"n\" : 1 }\n", "json-lines", "No readable JSON changes (formatting differs; see raw diff).\n"},
		{"duplicate keys", "f.json", `{"n":1,"n":2}`, `{"n":3}`, "raw-lines", "Raw line fallback: JSON is invalid, has duplicate keys, or exceeds nesting limits.\n"},
		{"nested escaped duplicate", "f.json", `{"a":[{"n":1,"\u006e":2}]}`, `{}`, "raw-lines", `-{"a":[{"n":1,"\u006e":2}]}`},
		{"invalid JSON", "f.json", `{"n":`, `{"n":1}`, "raw-lines", `-{"n":`},
		{"JSON newline", "f.json", "{\"n\":1}", "{\"n\":1}\n", "json-lines", "-}\n\\ No newline at end of file\n+}\n"},
		{"YAML lines", "f.yaml", "title: old\nitems: [one]\n", "title: new\nitems: [one]\n", "yaml-lines", "-title: old\n+title: new\n items: [one]\n"},
		{"text newline", "f.txt", "hello", "hello\n", "text-lines", "-hello\n\\ No newline at end of file\n+hello\n"},
		{"unchanged", "f.json", `{}`, `{}`, "json-lines", "No content changes.\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, kind := documentComparison(tc.path, tc.before, tc.after)
			if kind != tc.kind || !strings.Contains(got, tc.want) {
				t.Fatalf("kind=%q comparison=%q; want kind=%q containing %q", kind, got, tc.kind, tc.want)
			}
			if again, againKind := documentComparison(tc.path, tc.before, tc.after); again != got || againKind != kind {
				t.Fatal("comparison is not deterministic")
			}
		})
	}
}

func TestDocumentComparisonBounds(t *testing.T) {
	// One table covers raw input, indentation expansion, nesting and the final
	// combined display. Fallback must keep the original raw evidence intact.
	for _, tc := range []struct {
		name, before, after string
		fallback            bool
	}{
		{"input at limit", "{}", strings.Repeat(" ", 65533) + `"x"`, false},
		{"input over limit", "{}", strings.Repeat(" ", 65534) + `"x"`, true},
		{"expanded below limit", "[]", `["` + strings.Repeat("x", 65527) + `"]`, false},
		{"expanded at limit", "[]", `["` + strings.Repeat("x", 65528) + `"]`, false},
		{"expanded over limit", "[]", `["` + strings.Repeat("x", 65529) + `"]`, true},
		{"depth at limit", "[]", strings.Repeat("[", 100) + "0" + strings.Repeat("]", 100), false},
		{"depth over limit", "[]", strings.Repeat("[", 101) + "0" + strings.Repeat("]", 101), true},
		{"combined output", "[" + strings.Repeat("0,", 11999) + "0]", "[" + strings.Repeat("1,", 11999) + "1]", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, kind := documentComparison("f.json", tc.before, tc.after)
			if (kind == "raw-lines") != tc.fallback {
				t.Fatalf("kind=%q; want fallback=%t", kind, tc.fallback)
			}
			if tc.fallback {
				if !strings.HasPrefix(got, "Raw line fallback:") || !strings.HasSuffix(got, documentDiff("f.json", tc.before, tc.after)) {
					t.Fatal("fallback hides original diff or its reason")
				}
			} else if len(got) > 128<<10 {
				t.Fatal("expanded comparison exceeds display bound")
			}
		})
	}
}
