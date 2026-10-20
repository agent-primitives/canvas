package canvas

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRemoteSessionsDisablePlaintextDiagnosticsWithoutAuthentication(t *testing.T) {
	c := &Canvas{address: "127.0.0.1:1"}
	c.EnableRemoteSessions()
	if c.authenticated {
		t.Fatal("trusted transport must not imply verified identity")
	}
	if _, err := c.Verify(t.Context(), "missing"); !errors.Is(err, errRemoteDiagnostics) {
		t.Fatalf("remote diagnostics not disabled: %v", err)
	}
	mux := http.NewServeMux()
	c.announcementRoutes(mux, func(w http.ResponseWriter, code int, value any) {
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(value)
	})
	req := httptest.NewRequest("POST", "/api/announcement/verify", strings.NewReader(`{"id":"missing"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "no plaintext") {
		t.Fatalf("announcement diagnostics not disabled: %d %s", w.Code, w.Body.String())
	}
}
