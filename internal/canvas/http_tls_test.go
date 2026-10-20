package canvas

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestTLSLoopbackHandlerRejectsNoncanonicalOrigins(t *testing.T) {
	for _, origin := range []string{
		"", "http://127.0.0.1:8443", "https://localhost:8443", "https://0.0.0.0:8443", "https://[::1]:8443",
		"https://127.0.0.1", "https://127.0.0.1:0", "https://127.0.0.1:65536", "https://127.0.0.1:-1", "https://127.0.0.1:+443", "https://127.0.0.1:0443", "https://127.0.0.1:abc", "https://127.0.0.1:",
		"https://user@127.0.0.1:8443", "https://127.0.0.1:8443/", "https://127.0.0.1:8443/path", "https://127.0.0.1:8443?x=y", "https://127.0.0.1:8443?", "https://127.0.0.1:8443#x", "https://127.0.0.1:8443#",
		"HTTPS://127.0.0.1:8443", " https://127.0.0.1:8443", "https://127.000.000.001:8443", "https://127.0.0.1:8443\n",
	} {
		t.Run(origin, func(t *testing.T) {
			h, err := TLSLoopbackHandler(origin, http.NotFoundHandler())
			if err == nil || h != nil {
				t.Fatalf("accepted unsafe origin %q", origin)
			}
		})
	}
	for _, origin := range []string{"https://127.0.0.1:1", "https://127.0.0.1:65535", "https://127.0.0.1:8443"} {
		if _, err := TLSLoopbackHandler(origin, http.NotFoundHandler()); err != nil {
			t.Fatalf("valid origin %q: %v", origin, err)
		}
	}
}

func TestTLSLoopbackHandlerDefaultHTTPSPort(t *testing.T) {
	// WHATWG URL / browser origins (including Node's HTTPS adapter) omit :443.
	const origin = "https://127.0.0.1"
	calls := 0
	h, err := TLSLoopbackHandler(origin+":443", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := httptest.NewRecorder()
	h.ServeHTTP(bootstrap, httptest.NewRequest(http.MethodGet, origin+"/", nil))
	if bootstrap.Code != http.StatusNoContent {
		t.Fatalf("canonical bootstrap: %d", bootstrap.Code)
	}
	cookies := bootstrap.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure {
		t.Fatal("secure bootstrap cookie missing")
	}
	for _, tc := range []struct {
		host, origin string
		status       int
	}{
		{"127.0.0.1", origin, http.StatusNoContent},
		{"127.0.0.1:443", origin, http.StatusForbidden},
		{"127.0.0.1", origin + ":443", http.StatusForbidden},
		{"127.0.0.1:444", origin, http.StatusForbidden},
		{"127.0.0.1", origin + ":444", http.StatusForbidden},
	} {
		r := httptest.NewRequest(http.MethodPost, origin+"/api/run", nil)
		r.Host = tc.host
		r.Header.Set("Origin", tc.origin)
		r.AddCookie(cookies[0])
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("host=%s origin=%s: %d", tc.host, tc.origin, w.Code)
		}
	}
	if calls != 2 {
		t.Fatalf("endpoint calls=%d", calls)
	}
}

// Pin LocalHandler's existing bytes except its random token; hosted differs only
// by Secure and its explicit origin, not by CSP, cookie scope, or proxy trust.
func TestBrowserGuardsHeadersCookiesAndBoundary(t *testing.T) {
	const host = "127.0.0.1:18443"
	for _, secure := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "TLS"}[secure], func(t *testing.T) {
			scheme := "http"
			if secure {
				scheme = "https"
			}
			origin := scheme + "://" + host
			calls := 0
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusNoContent) })
			var h http.Handler
			if secure {
				var err error
				h, err = TLSLoopbackHandler(origin, next)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				h = LocalHandler(host, next)
			}
			bootstrap := httptest.NewRecorder()
			req := httptest.NewRequest("GET", origin+"/", nil)
			// The pod's local socket is not the browser's port-forward origin.
			req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8443}))
			req.Header.Set("X-Forwarded-Host", "attacker.invalid")
			req.Header.Set("X-Forwarded-Proto", "http")
			req.Header.Set("Forwarded", "host=attacker.invalid;proto=http")
			h.ServeHTTP(bootstrap, req)
			if bootstrap.Code != http.StatusNoContent || calls != 1 {
				t.Fatalf("bootstrap: %d", bootstrap.Code)
			}
			cookies := bootstrap.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatalf("cookies: %v", cookies)
			}
			cookie := cookies[0]
			if len(cookie.Value) != 40 || strings.Trim(cookie.Value, "0123456789abcdef") != "" {
				t.Fatal("unexpected token")
			}
			wantCookie := "canvas-local=" + cookie.Value + "; Path=/; HttpOnly"
			if secure {
				wantCookie += "; Secure"
			}
			wantCookie += "; SameSite=Strict"
			wantHeaders := http.Header{
				"Content-Security-Policy": {"default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"},
				"X-Content-Type-Options":  {"nosniff"}, "Referrer-Policy": {"no-referrer"}, "Cache-Control": {"no-store"}, "Set-Cookie": {wantCookie},
			}
			if !reflect.DeepEqual(bootstrap.Header(), wantHeaders) {
				t.Fatalf("headers:\n got %v\nwant %v", bootstrap.Header(), wantHeaders)
			}
			for _, tc := range []struct {
				name, method, host, origin, site string
				cookie                           *http.Cookie
				want                             int
			}{
				{"valid mutation", "POST", host, origin, "same-origin", cookie, 204},
				{"HEAD bootstrap", "HEAD", host, "", "", nil, 204},
				{"missing Host", "GET", "", "", "", nil, 403},
				{"wrong Host", "POST", "127.0.0.1:8443", origin, "", cookie, 403},
				{"foreign Host", "GET", "attacker.invalid", "", "", nil, 403},
				{"missing Origin", "POST", host, "", "", cookie, 403},
				{"wrong Origin", "POST", host, "https://attacker.invalid", "", cookie, 403},
				{"wrong scheme", "POST", host, "ftp://" + host, "", cookie, 403},
				{"missing cookie", "POST", host, origin, "", nil, 403},
				{"wrong cookie", "POST", host, origin, "", &http.Cookie{Name: "canvas-local", Value: "wrong"}, 403},
				{"cross site read", "GET", host, "", "cross-site", nil, 403},
				{"cross site mutation", "POST", host, origin, "cross-site", cookie, 403},
			} {
				t.Run(tc.name, func(t *testing.T) {
					r := httptest.NewRequest(tc.method, origin+"/api/run", nil)
					r.Host = tc.host
					r.Header.Set("Origin", tc.origin)
					r.Header.Set("Sec-Fetch-Site", tc.site)
					// Forwarded headers must neither repair invalid trust nor break valid trust.
					r.Header.Set("X-Forwarded-Host", host)
					r.Header.Set("X-Forwarded-Proto", scheme)
					r.Header.Set("X-Forwarded-Port", "18443")
					r.Header.Set("X-Forwarded-Origin", origin)
					r.Header.Set("Forwarded", "host="+host+";proto="+scheme)
					if tc.cookie != nil {
						r.AddCookie(tc.cookie)
					}
					before := calls
					w := httptest.NewRecorder()
					h.ServeHTTP(w, r)
					if w.Code != tc.want {
						t.Fatalf("status %d want %d", w.Code, tc.want)
					}
					if tc.want == 403 && (calls != before || w.Header().Get("Set-Cookie") != "") {
						t.Fatal("refusal reached endpoint or minted cookie")
					}
					if tc.want == 204 && calls != before+1 {
						t.Fatal("valid request did not reach endpoint")
					}
					for _, name := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy", "Cache-Control"} {
						if !reflect.DeepEqual(w.Header().Values(name), wantHeaders.Values(name)) {
							t.Fatalf("missing security header %s", name)
						}
					}
				})
			}
			other := LocalHandler(host, next)
			if secure {
				var err error
				other, err = TLSLoopbackHandler(origin, next)
				if err != nil {
					t.Fatal(err)
				}
			}
			r := httptest.NewRequest("POST", origin+"/", nil)
			r.Header.Set("Origin", origin)
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			other.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatal("different handler accepted another handler's token")
			}
		})
	}
}
