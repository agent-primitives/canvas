package canvas

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnnouncementHTTPStrictBoundaryAndExplicitDecision(t *testing.T) {
	c, _, calls := definedFixtureAt(t, actionDefinitionFixture(t), nil)
	server := newTestServer(t, c)
	for _, body := range []string{`null`, `{"role":"GM","role":"Lead"}`, `{"Role":"GM"}`, `{"role":"GM","extra":true}`, `{} {}`, `{"id":"missing","role":"GM"}`, `{"approved":null}`, `{"confirmation":{"Head":1}}`} {
		if code, _ := server.do("POST", "/api/announcement/decision", body); code != 400 {
			t.Fatalf("invalid body %s: %d", body, code)
		}
	}
	p := acceptedAnnouncement(t, c, "# HTTP exact snapshot\n")
	body, _ := json.Marshal(map[string]string{"action": "announce", "proposal": p.ID, "role": "GM"})
	post := httptest.NewRequest("POST", "http://127.0.0.1:8765/api/announcement/request", strings.NewReader(string(body)))
	post.Header.Set("Origin", "http://127.0.0.1:8765")
	post.AddCookie(server.cookie)
	out := httptest.NewRecorder()
	server.h.ServeHTTP(out, post)
	code, raw := out.Code, out.Body.String()
	if code != 200 {
		t.Fatalf("request: %d %s", code, raw)
	}
	var response struct {
		ID string `json:"id"`
	}
	if json.Unmarshal([]byte(raw), &response) != nil || response.ID == "" {
		t.Fatal(raw)
	}
	v := confirmedAnnouncement(t, c, response.ID)
	body, _ = json.Marshal(map[string]any{"id": response.ID, "role": "GM", "approved": true, "confirmation": v.Confirmation})
	request := httptest.NewRequest("POST", "http://127.0.0.1:8765/api/announcement/decision", strings.NewReader(string(body)))
	request.Header.Set("Origin", "https://external.invalid")
	request.AddCookie(server.cookie)
	recorder := httptest.NewRecorder()
	server.h.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatal("origin bypass")
	}
	if code, raw := server.do("POST", "/api/announcement/decision", string(body)); code != 200 {
		t.Fatalf("decision %d %s", code, raw)
	}
	v = confirmedAnnouncement(t, c, response.ID)
	if v.Status != "approved" || v.Concluded || v.Receipt != nil {
		t.Fatal("decision sent anything")
	}
	if calls.Load() != 0 {
		t.Fatal("HTTP caused model work")
	}
}

func TestAnnouncementHTTPReplayRequiresCompletedExactSnapshot(t *testing.T) {
	c, _, _ := definedFixtureAt(t, actionDefinitionFixture(t), nil)
	p := acceptedAnnouncement(t, c, "# Replay snapshot\n")
	id, err := c.RequestAnnouncement(t.Context(), "announce", p.ID, "GM")
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServer(t, c)
	body, _ := json.Marshal(map[string]string{"id": id})
	if code, _ := server.do("POST", "/api/announcement/verify", string(body)); code != 409 {
		t.Fatalf("pending verification: %d", code)
	}
	v := confirmedAnnouncement(t, c, id)
	if err := c.DecideAnnouncement(t.Context(), id, "GM", true, v.Confirmation); err != nil {
		t.Fatal(err)
	}
	v = confirmedAnnouncement(t, c, id)
	if err := c.ContinueAnnouncement(t.Context(), id, "GM", v.Confirmation); err != nil {
		t.Fatal(err)
	}
	if code, message := server.do("POST", "/api/announcement/verify", string(body)); code != 200 {
		t.Fatalf("completed verification: %d %s", code, message)
	}
}

func TestAnnouncementHTTPExplainsBoundedRequestRefusal(t *testing.T) {
	c, _, _ := definedFixtureAt(t, actionDefinitionFixture(t), nil)
	p := acceptedAnnouncement(t, c, "# Capacity test\n")
	c.mu.Lock()
	c.data.Announcements = make([]announcementOperation, 128)
	c.mu.Unlock()
	server := newTestServer(t, c)
	body, _ := json.Marshal(map[string]string{"action": "announce", "proposal": p.ID, "role": "GM"})
	if code, message := server.do("POST", "/api/announcement/request", string(body)); code != 409 || message != errAnnouncementLimit.Error() {
		t.Fatalf("wrong capacity refusal: %d %s", code, message)
	}
}
