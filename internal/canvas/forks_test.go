package canvas

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestForkAncestryReadsOnlyRecordedSessionAndDocumentIdentity(t *testing.T) {
	// No Sessions client or document store: the endpoint must read only records.
	c := &Canvas{definition: &CompiledDefinition{}, data: ledger{Attempts: []attempt{
		{Run: Run{ID: "parent", Session: "parent-session", Base: "parent-document-base", Status: "proposed"}},
		{Run: Run{ID: "child", Session: "child-session", ParentSession: "parent-session", ForkSeq: 17, Base: "child-document-base", Status: "uncertain"}},
		{Run: Run{ID: "incomplete-child", ParentSession: "parent-session", ForkSeq: 17, Base: "child-document-base", Status: "failed"}},
		{Run: Run{ID: "unrelated", Session: "unrelated-session", Base: "unrelated-base", Status: "proposed"}},
	}}}
	h := c.APIHandler(apiTestHost)
	for _, handler := range []http.Handler{h, c.Handler(apiTestHost, nil)} {
		w := apiRequest(handler, nil, "GET", "/api/forks?run=parent", "")
		var parent ForkAncestry
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &parent) != nil {
			t.Fatalf("parent: %d %s", w.Code, w.Body.String())
		}
		if parent.Run != "parent" || parent.Session != "parent-session" || parent.Parent.Available || parent.Parent.Reason == "" || parent.Parent.Session != "" || parent.Parent.ForkSeq != 0 {
			t.Fatalf("unparented run acquired ancestry: %+v", parent)
		}
		if !parent.Document.Available || parent.Document.Base != "parent-document-base" || len(parent.Children) != 2 {
			t.Fatalf("parent document/children: %+v", parent)
		}
		child := parent.Children[0]
		if child.Run != "child" || child.Session != "child-session" || child.Status != "uncertain" || child.ForkSeq != 17 || !child.Available || child.Document.Base != "child-document-base" {
			t.Fatalf("child mapping: %+v", child)
		}
		if parent.Children[1].Available || parent.Children[1].Session != "" || parent.Children[1].Reason == "" {
			t.Fatalf("invented incomplete child: %+v", parent.Children[1])
		}
		w = apiRequest(handler, nil, "GET", "/api/forks?run=child", "")
		var fork ForkAncestry
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &fork) != nil || !fork.Parent.Available || fork.Parent.Session != "parent-session" || fork.Parent.ForkSeq != 17 || fork.Document.Base != "child-document-base" || len(fork.Children) != 0 {
			t.Fatalf("child ancestry: %d %s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "parent-document-base") {
			t.Fatal("parent checkpoint was conflated with document base")
		}
	}
}

func TestForkAncestryUnavailableFieldsAreNotFilledFromOtherRecords(t *testing.T) {
	for _, run := range []Run{
		{ID: "missing-session", ParentSession: "recorded-parent", ForkSeq: 9, Base: "base"},
		{ID: "missing-parent", Session: "session", ForkSeq: 9, Base: "base"},
		{ID: "missing-checkpoint", Session: "session", ParentSession: "recorded-parent", Base: "base"},
		{ID: "negative-checkpoint", Session: "session", ParentSession: "recorded-parent", ForkSeq: -1, Base: "base"},
	} {
		t.Run(run.ID, func(t *testing.T) {
			c := &Canvas{definition: &CompiledDefinition{}, data: ledger{Attempts: []attempt{{Run: run}}}}
			w := apiRequest(c.APIHandler(apiTestHost), nil, "GET", "/api/forks?run="+run.ID, "")
			var v ForkAncestry
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Parent.Available || v.Parent.Reason == "" || v.Parent.Session != run.ParentSession || v.Parent.ForkSeq != run.ForkSeq {
				t.Fatalf("incomplete ancestry fabricated: %d %s", w.Code, w.Body.String())
			}
		})
	}
	c := &Canvas{definition: &CompiledDefinition{}, data: ledger{Attempts: []attempt{{Run: Run{ID: "missing-base", Session: "child", ParentSession: "parent", ForkSeq: 5}}}}}
	w := apiRequest(c.APIHandler(apiTestHost), nil, "GET", "/api/forks?run=missing-base", "")
	var v ForkAncestry
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || !v.Parent.Available || v.Document.Available || v.Document.Base != "" || v.Document.Reason == "" {
		t.Fatalf("missing document base inferred from session checkpoint: %d %s", w.Code, w.Body.String())
	}
}

func TestForkAncestryUnknownRunAndGetOnly(t *testing.T) {
	c := &Canvas{definition: &CompiledDefinition{}, data: ledger{Attempts: []attempt{{Run: Run{ID: "recorded", Session: "session", Base: "base"}}}}}
	h := c.APIHandler(apiTestHost)
	w := apiRequest(h, nil, "GET", "/api/forks?run=recorded", "")
	cookie := w.Result().Cookies()[0]
	for _, path := range []string{"/api/forks", "/api/forks?run=unknown"} {
		if w := apiRequest(h, cookie, "GET", path, ""); w.Code != 404 {
			t.Errorf("unknown run: %s %d", path, w.Code)
		}
	}
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		w := apiRequest(h, cookie, method, "/api/forks?run=recorded", `{}`)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s forks: %d %s", method, w.Code, w.Body.String())
		}
	}
	if len(c.data.Attempts) != 1 || c.data.Attempts[0].Session != "session" || c.data.Attempts[0].Status != "" {
		t.Fatal("fork inspection changed recorded runs")
	}
}
