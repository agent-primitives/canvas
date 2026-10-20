package canvas

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const apiTestHost = "127.0.0.1:8765"

func apiRequest(h http.Handler, cookie *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+apiTestHost+path, strings.NewReader(body))
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if method != http.MethodGet && method != http.MethodHead {
		r.Header.Set("Origin", "http://"+apiTestHost)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func apiCookie(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	w := apiRequest(h, nil, "GET", "/api/state", "")
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatalf("bootstrap: %d %s", w.Code, w.Body.String())
	}
	return w.Result().Cookies()[0]
}

func TestHeadlessAPIExplicitGenericSurface(t *testing.T) {
	c, _, calls := definedFixture(t, nil)
	h := c.APIHandler(apiTestHost)
	cookie := apiCookie(t, h)
	for _, path := range []string{"/", "/app.js", "/style.css", "/football", "/api/resume", "/api/scenarios", "/api/role", "/api/comment", "/api/run/extra", "/api/unknown"} {
		for _, method := range []string{"GET", "POST"} {
			w := apiRequest(h, cookie, method, path, `{}`)
			if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s exposed: %d %s", method, path, w.Code, w.Body.String())
			}
		}
	}
	w := apiRequest(h, nil, "GET", "/api/state", "")
	var state map[string]json.RawMessage
	if json.Unmarshal(w.Body.Bytes(), &state) != nil {
		t.Fatal(w.Body.String())
	}
	for _, key := range []string{"definition", "agents", "people", "documents", "head", "runs", "proposals", "history", "triggers", "errors"} {
		if _, ok := state[key]; !ok {
			t.Errorf("generic state lost %q", key)
		}
	}
	for _, key := range []string{"prospects", "role", "conversation", "spacePath"} {
		if _, ok := state[key]; ok {
			t.Errorf("headless state exposes browser-only %q", key)
		}
	}
	browser := c.Handler(apiTestHost, fstest.MapFS{"index.html": {Data: []byte("browser")}})
	w = apiRequest(browser, nil, "GET", "/api/state", "")
	if json.Unmarshal(w.Body.Bytes(), &state) != nil || string(state["prospects"]) != "[]" || state["role"] == nil || state["conversation"] == nil || state["spacePath"] == nil {
		t.Fatal("browser state compatibility changed")
	}
	if calls.Load() != 0 {
		t.Fatal("headless inspection performed model work")
	}
}

func TestBrowserAndMountedHeadlessShareOneCookie(t *testing.T) {
	c, _, _ := definedFixture(t, nil)
	h := c.Handler(apiTestHost, fstest.MapFS{"index.html": {Data: []byte("browser")}})
	browser := apiRequest(h, nil, "GET", "/api/state", "")
	cookie := browser.Result().Cookies()[0]
	headless := apiRequest(h, cookie, "GET", "/engine/api/state", "")
	if headless.Code != 200 {
		t.Fatalf("headless not mounted: %d", headless.Code)
	}
	if headless.Result().Cookies()[0].Value != cookie.Value {
		t.Fatal("separate guards minted inconsistent cookies")
	}
	var state map[string]json.RawMessage
	if json.Unmarshal(headless.Body.Bytes(), &state) != nil || state["role"] != nil {
		t.Fatal("headless leaked browser role")
	}
	denied := apiRequest(h, cookie, "POST", "/engine/api/scenarios", `{}`)
	if denied.Code != 404 && denied.Code != 405 {
		t.Fatal("legacy mutation entered headless surface")
	}
	for _, path := range []string{"/engine/", "/engine/app.js"} {
		if apiRequest(h, cookie, "GET", path, "").Code != 404 {
			t.Fatal("headless serves assets")
		}
	}
	invalid := apiRequest(h, cookie, "POST", "/engine/api/run", `{"agent":"unknown","request":"x","role":"GM"}`)
	if invalid.Code == 403 {
		t.Fatal("shared browser cookie failed headless CSRF check")
	}
}

func TestHeadlessAPIGuardAndStrictRequests(t *testing.T) {
	c, _, calls := definedFixture(t, nil)
	h := c.APIHandler(apiTestHost)
	cookie := apiCookie(t, h)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" {
		t.Fatal("headless cookie weaker than browser cookie")
	}
	for _, tc := range []struct {
		name, host, origin, site string
		cookie                   *http.Cookie
	}{
		{"missing cookie", apiTestHost, "http://" + apiTestHost, "", nil},
		{"wrong cookie", apiTestHost, "http://" + apiTestHost, "", &http.Cookie{Name: "canvas-local", Value: "wrong"}},
		{"wrong host", "evil.invalid", "http://" + apiTestHost, "", cookie},
		{"missing origin", apiTestHost, "", "", cookie},
		{"wrong origin", apiTestHost, "https://evil.invalid", "", cookie},
		{"cross site", apiTestHost, "http://" + apiTestHost, "cross-site", cookie},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "http://"+tc.host+"/api/run", strings.NewReader(`{"agent":"writer","request":"Draft.","role":"GM"}`))
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Sec-Fetch-Site", tc.site)
			if tc.cookie != nil {
				r.AddCookie(tc.cookie)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 403 || w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("guard: %d %+v", w.Code, w.Header())
			}
		})
	}
	for _, body := range []string{`null`, `{`, `{} {}`, `{"agent":"writer","request":"Draft.","role":"GM","extra":true}`, `{"agent":"writer","request":"Draft.","role":"GM","prospect":"legacy"}`, `{"agent":"writer","request":"` + strings.Repeat("x", 8192) + `","role":"GM"}`} {
		w := apiRequest(h, cookie, "POST", "/api/run", body)
		if w.Code != 400 {
			t.Errorf("invalid action accepted: %d %s", w.Code, w.Body.String())
		}
	}
	fresh := c.APIHandler(apiTestHost)
	w := apiRequest(fresh, cookie, "POST", "/api/run", `{"agent":"writer","request":"Draft.","role":"GM"}`)
	if w.Code != 403 {
		t.Fatal("prior guard cookie accepted after restart")
	}
	if calls.Load() != 0 {
		t.Fatal("rejected request caused model work")
	}
}

func TestHeadlessAPIRunDocumentReviewAndVerify(t *testing.T) {
	c, _, calls := definedFixture(t, nil)
	h := c.APIHandler(apiTestHost)
	cookie := apiCookie(t, h)
	w := apiRequest(h, cookie, "POST", "/api/run", `{"agent":"writer","request":"Draft fictional requirements.","role":"GM"}`)
	var response struct {
		Run string `json:"run"`
	}
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Run == "" {
		t.Fatalf("run: %d %s", w.Code, w.Body.String())
	}
	run := completed(t, c, response.Run)
	w = apiRequest(h, cookie, "GET", "/api/document?path=prd.json&proposal="+run.Proposal, "")
	var doc map[string]string
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &doc) != nil || doc["baseCommit"] != run.Base || doc["accepted"] == "" {
		t.Fatalf("document: %d %s", w.Code, w.Body.String())
	}
	w = apiRequest(h, cookie, "POST", "/api/review", `{"proposal":"`+run.Proposal+`","action":"accept","role":"GM"}`)
	if w.Code != 200 {
		t.Fatalf("review: %d %s", w.Code, w.Body.String())
	}
	before := calls.Load()
	w = apiRequest(h, cookie, "POST", "/api/verify", `{"run":"`+run.ID+`"}`)
	var proof struct {
		ChainValid    bool `json:"chainValid"`
		Reconstructed bool `json:"reconstructed"`
		ModelCalls    int  `json:"modelCalls"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &proof) != nil || !proof.ChainValid || !proof.Reconstructed || proof.ModelCalls != 0 || calls.Load() != before {
		t.Fatalf("verify: %d %s", w.Code, w.Body.String())
	}
	w = apiRequest(h, cookie, "GET", "/api/document?path=prd.json&proposal=unknown", "")
	if w.Code != 404 {
		t.Fatalf("missing proposal: %d", w.Code)
	}
	w = apiRequest(h, cookie, "POST", "/api/verify", `{"run":"unknown"}`)
	if w.Code != 409 {
		t.Fatalf("missing proof changed semantics: %d", w.Code)
	}
}

func TestHeadlessAPIRejectsLegacyCanvas(t *testing.T) {
	c := &Canvas{}
	h := c.APIHandler(apiTestHost)
	for _, path := range []string{"/api/state", "/api/document?path=strategy.md", "/api/forks?run=missing"} {
		if w := apiRequest(h, nil, "GET", path, ""); w.Code != 404 {
			t.Errorf("legacy canvas exposed through API: %s %d", path, w.Code)
		}
	}
}

func TestSharedLocalGuardComposesAssetsAPIRoutesAndCommandReads(t *testing.T) {
	c, _, calls := definedFixture(t, nil)
	mux := http.NewServeMux()
	c.RegisterAPIRoutes(mux)
	mux.HandleFunc("GET /api/demo", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"boot":"test"}`)) })
	mux.Handle("/", AssetHandler(fstest.MapFS{"index.html": {Data: []byte("browser")}, "app.js": {Data: []byte("script")}}))
	h := LocalHandler(apiTestHost, mux)
	w := apiRequest(h, nil, "GET", "/", "")
	if w.Code != 200 || w.Body.String() != "browser" || len(w.Result().Cookies()) != 1 {
		t.Fatalf("assets: %d %s", w.Code, w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	for _, path := range []string{"/api/state", "/api/demo", "/app.js"} {
		w = apiRequest(h, cookie, "GET", path, "")
		if w.Code != 200 || len(w.Result().Cookies()) != 1 || w.Result().Cookies()[0].Value != cookie.Value {
			t.Fatalf("inconsistent composed guard: %s %d %+v", path, w.Code, w.Result().Cookies())
		}
	}
	w = apiRequest(h, cookie, "POST", "/api/review", `{"proposal":"missing","action":"accept","role":"GM"}`)
	if w.Code != 404 {
		t.Fatalf("asset cookie rejected by API: %d %s", w.Code, w.Body.String())
	}
	w = apiRequest(h, cookie, "POST", "/api/scenarios", `{}`)
	if w.Code != 405 && w.Code != 404 {
		t.Fatalf("browser fallback exposed scenarios: %d", w.Code)
	}
	if calls.Load() != 0 {
		t.Fatal("composition inspected with model work")
	}
}

func TestHeadlessAnnouncementRetainsSeparateApprovalAndContinue(t *testing.T) {
	c, _, calls := definedFixtureAt(t, actionDefinitionFixture(t), nil)
	p := acceptedAnnouncement(t, c, "# Headless exact snapshot\n")
	h := c.APIHandler(apiTestHost)
	cookie := apiCookie(t, h)
	w := apiRequest(h, cookie, "POST", "/api/announcement/request", `{"action":"announce","proposal":"`+p.ID+`","role":"GM"}`)
	var response struct {
		ID string `json:"id"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.ID == "" {
		t.Fatalf("announcement request: %d %s", w.Code, w.Body.String())
	}
	v := confirmedAnnouncement(t, c, response.ID)
	w = apiRequest(h, cookie, "POST", "/api/announcement/decision", `{"id":"`+response.ID+`","role":"GM","approved":true,"extra":true}`)
	if w.Code != 400 {
		t.Fatalf("announcement strict decode: %d", w.Code)
	}
	body, _ := json.Marshal(map[string]any{"id": response.ID, "role": "GM", "approved": true, "confirmation": v.Confirmation})
	w = apiRequest(h, cookie, "POST", "/api/announcement/decision", string(body))
	if w.Code != 200 {
		t.Fatalf("decision: %d %s", w.Code, w.Body.String())
	}
	v = confirmedAnnouncement(t, c, response.ID)
	if v.Status != "approved" || v.Concluded || v.Receipt != nil {
		t.Fatal("approval implicitly continued action")
	}
	w = apiRequest(h, cookie, "GET", "/api/announcement?id="+response.ID, "")
	if w.Code != 200 || calls.Load() != 0 {
		t.Fatalf("inspect: %d model calls=%d", w.Code, calls.Load())
	}
}
