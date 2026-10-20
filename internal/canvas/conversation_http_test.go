package canvas

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/agent-primitives/canvas/internal/contract"
)

func TestConversationActionsUseBrowserBoundaryAndPublicStateHidesQuestionInput(t *testing.T) {
	var calls atomic.Int32
	c, close := questionCanvas(t, `{"answer":"The recorded assessment used synthetic production and explicitly noted missing medical evidence."}`, &calls)
	defer close()
	id, err := c.Manual("writer", "", "Assess evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	completed(t, c, id)
	host := "127.0.0.1:8765"
	handler := c.Handler(host, fstest.MapFS{"index.html": {Data: []byte("Draft Room")}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "http://"+host+"/", nil))
	cookie := response.Result().Cookies()[0]
	post := func(path, body, origin string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("POST", "http://"+host+path, strings.NewReader(body))
		request.Header.Set("Origin", origin)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	for _, path := range []string{"/api/comment", "/api/ask"} {
		if got := post(path, `{"run":"`+id+`","request":"Why?","role":"coach"}`, "http://other.invalid"); got.Code != http.StatusForbidden {
			t.Fatal("cross-origin room action accepted")
		}
	}
	response = post("/api/comment", `{"request":"Please clarify our priorities.","role":"owner"}`, "http://"+host)
	if response.Code != 200 || calls.Load() != 1 {
		t.Fatal("comment endpoint did not stay separate from model execution")
	}
	response = post("/api/ask", `{"run":"`+id+`","request":"Why retain the rank?","role":"coach"}`, "http://"+host)
	if response.Code != 202 {
		t.Fatal("question endpoint failed")
	}
	var result struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(response.Body.Bytes(), &result) != nil {
		t.Fatal("question response unreadable")
	}
	awaitQuestion(t, c, result.Message)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "http://"+host+"/api/state", nil))
	if response.Code != 200 || strings.Contains(response.Body.String(), "originalInput") || strings.Contains(response.Body.String(), "expectedSeq") || strings.Contains(response.Body.String(), "harness\"") {
		t.Fatal("public conversation exposed private model input or execution intent")
	}
	if !strings.Contains(response.Body.String(), "Why retain the rank?") || !strings.Contains(response.Body.String(), "priorities") {
		t.Fatal("public room messages missing")
	}
}

func TestVerifyHTTPReportsQuestionBusyInsteadOfMissingEvidence(t *testing.T) {
	var calls atomic.Int32
	c, close := questionCanvas(t, `{"answer":"Synthetic explanation."}`, &calls)
	defer close()
	id, err := c.Manual("writer", "", "Assess evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	completed(t, c, id)
	c.wg.Wait()
	c.sem <- struct{}{}
	defer func() { <-c.sem }()
	if _, err := c.Ask(id, "Why?", "GM"); err != nil {
		t.Fatal(err)
	}
	host := "127.0.0.1:8765"
	handler := c.Handler(host, fstest.MapFS{"index.html": {Data: []byte("Draft Room")}})
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest("GET", "http://"+host+"/", nil))
	request := httptest.NewRequest("POST", "http://"+host+"/api/verify", strings.NewReader(`{"run":"`+id+`"}`))
	request.Header.Set("Origin", "http://"+host)
	request.AddCookie(get.Result().Cookies()[0])
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 409 || !strings.Contains(response.Body.String(), "current session turn") {
		t.Fatal("busy session mislabeled as unavailable reconstruction evidence")
	}
}

func TestLegacyQuestionHTTPIsExplicitlyUnsupported(t *testing.T) {
	var calls atomic.Int32
	c, close := questionCanvas(t, `{"answer":"not used"}`, &calls)
	defer close()
	id, err := c.Manual("writer", "", "Assess evidence.", "GM")
	if err != nil {
		t.Fatal(err)
	}
	completed(t, c, id)
	c.wg.Wait()
	// Synthetic historical binding; no legacy workspace is activated.
	c.mu.Lock()
	a := c.findLocked(id)
	a.Harness = contract.HarnessName(a.BundleDigest, 2)
	c.mu.Unlock()
	host := "127.0.0.1:8765"
	handler := c.Handler(host, fstest.MapFS{"index.html": {Data: []byte("Draft Room")}})
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest("GET", "http://"+host+"/", nil))
	request := httptest.NewRequest("POST", "http://"+host+"/api/ask", strings.NewReader(`{"run":"`+id+`","request":"Why?","role":"owner"}`))
	request.Header.Set("Origin", "http://"+host)
	request.AddCookie(get.Result().Cookies()[0])
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 409 || !strings.Contains(response.Body.String(), "unsupported") || calls.Load() != 1 {
		t.Fatal("legacy HTTP action spent a call or hid unsupported behavior")
	}
}
