package canvas

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalCookieRefusesRebindingAndCrossSiteRequests(t *testing.T) {
	const host = "127.0.0.1:8765"
	calls := 0
	h := LocalHandler(host, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://"+host+"/", nil))
	cookies := w.Result().Cookies()
	if w.Code != http.StatusNoContent || len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("local CSRF cookie unavailable or weakened")
	}
	cookie := cookies[0]
	for _, tc := range []struct {
		name, method, host, origin, site string
		cookie                           *http.Cookie
	}{
		{"rebound read", "GET", "evil.invalid:8765", "", "", nil},
		{"cross-site read", "GET", host, "", "cross-site", nil},
		{"missing nonce", "POST", host, "http://" + host, "", nil},
		{"wrong nonce", "POST", host, "http://" + host, "", &http.Cookie{Name: "canvas-local", Value: "attacker"}},
		{"foreign origin", "POST", host, "http://evil.invalid", "", cookie},
		{"absent origin", "POST", host, "", "", cookie},
		{"cross-site mutation", "POST", host, "http://" + host, "cross-site", cookie},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://"+host+"/", nil)
			r.Host = tc.host
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Sec-Fetch-Site", tc.site)
			if tc.cookie != nil {
				r.AddCookie(tc.cookie)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden || len(w.Result().Cookies()) != 0 || calls != 1 {
				t.Fatal("attack reached the handler or obtained a CSRF nonce")
			}
		})
	}
}
