package canvas

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestBrowserBoundaryRejectsCrossOriginAndRebinding(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	h := c.Handler("127.0.0.1:8765", fstest.MapFS{"index.html": {Data: []byte("<h1>Draft Room</h1>")}})
	r := httptest.NewRequest("GET", "http://127.0.0.1:8765/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 || !w.Result().Cookies()[0].HttpOnly {
		t.Fatal("browser session boundary missing")
	}
	cookie := w.Result().Cookies()[0]
	for _, origin := range []string{"http://evil.invalid", ""} {
		r = httptest.NewRequest("POST", "http://127.0.0.1:8765/api/role", strings.NewReader(`{"role":"coach"}`))
		r.Header.Set("Origin", origin)
		r.AddCookie(cookie)
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatal("cross-origin mutation accepted")
		}
	}
	r = httptest.NewRequest("GET", "http://evil.invalid/api/state", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal("rebinding host accepted")
	}
	r = httptest.NewRequest("POST", "http://127.0.0.1:8765/api/role", strings.NewReader(`{"role":"coach"}`))
	r.Header.Set("Origin", "http://127.0.0.1:8765")
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("same-origin demo role action failed")
	}
	state, _ := c.State()
	if state.Role != "coach" {
		t.Fatal("demo role not recorded")
	}
}
func TestHTTPDoesNotExposePrivateJournalOrArbitraryFiles(t *testing.T) {
	c, close := setup(t, false)
	defer close()
	h := c.Handler("127.0.0.1:8765", fstest.MapFS{"index.html": {Data: []byte("Draft Room")}})
	for _, path := range []string{"/.state/attempts.json", "/DESIGN.md", "/api/document?path=../../outside"} {
		r := httptest.NewRequest("GET", "http://127.0.0.1:8765"+path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code == 200 {
			t.Fatal("private/arbitrary file served")
		}
	}
}
