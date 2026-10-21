package canvas

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEditHTTPStrictDecode(t *testing.T) {
	for _, body := range []string{
		`{"document":"prd","document":"other"}`, `{"Document":"prd"}`,
		`{"unknown":true}`, `{"section":{"kind":"json-pointer","pointer":"/title","headingPath":[]}}`,
		`{"section":{"kind":"markdown-heading","headingPath":["Title"],"occurrence":1,"pointer":""}}`,
		`{"section":{"kind":"json-pointer","pointer":"/title","Pointer":"/problem"}}`,
		`{"section":{"kind":"markdown-heading","headingPath":["Title"],"occurrence":1e9999}}`,
		`{"section":{"kind":"markdown-heading","headingPath":["Title"],"occurrence":1.5}}`,
		`null`, `{"section":null}`, `{} {}`, strings.Repeat(" ", 8193),
	} {
		t.Run(body[:min(len(body), 80)], func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/api/edit/request", strings.NewReader(body))
			var q EditRequest
			if decodeEditAction(w, r, &q) || w.Code != 400 || !strings.Contains(w.Body.String(), "EDIT_INVALID") {
				t.Fatalf("accepted invalid body: %d %s", w.Code, w.Body.String())
			}
		})
	}
	for _, body := range []string{`{"section":{"kind":"json-pointer","pointer":""}}`, `{"section":{"kind":"markdown-heading","headingPath":["Title"],"occurrence":1}}`} {
		var q EditRequest
		if !decodeEditAction(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(body)), &q) {
			t.Fatal("valid union refused", body)
		}
	}
}

func TestEditHTTPReadOnlyAndLocalGuard(t *testing.T) {
	c, calls := workflowFixture(t, false, func(map[string]any) string { return changeEnvelope("prd", validPRD) })
	mux := http.NewServeMux()
	c.registerEditRoutes(mux)
	for _, path := range []string{"/api/edits", "/api/edit/context?document=prd"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var view map[string]any
		if json.Unmarshal(w.Body.Bytes(), &view) != nil {
			t.Fatal("not JSON")
		}
		if strings.Contains(path, "context") && (view["baseRevision"] != mustHead(t, c) || view["baseDigest"] == "" || view["content"] == "") {
			t.Fatal(view)
		}
	}
	if calls.Load() != 0 || len(c.data.Edits) != 0 {
		t.Fatal("read dispatched or reconciled")
	}
	for _, path := range []string{"/api/edit/context?document=missing", "/api/edit/context?document=prd&flowRun=foreign"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 409 {
			t.Fatalf("unsafe context: %d", w.Code)
		}
	}
	for _, path := range []string{"/api/edit/resume", "/api/edit/rerun"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader("{}")))
		if w.Code != http.StatusNotFound {
			t.Fatalf("flow continuation route retained: %s: %d", path, w.Code)
		}
	}
	for _, remote := range []bool{false, true} {
		c.authenticated = !remote
		c.remoteSessions = remote
		for _, path := range []string{"/api/edits", "/api/edit?id=x", "/api/edit/context?document=prd", "/api/edit/request", "/api/edit/reconcile", "/api/edit/abandon"} {
			method := "GET"
			if strings.Contains(path, "/request") || strings.Contains(path, "/reconcile") || strings.Contains(path, "/abandon") {
				method = "POST"
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader("{}")))
			if w.Code != 404 {
				t.Fatalf("remote %s: %d", path, w.Code)
			}
		}
	}
}
