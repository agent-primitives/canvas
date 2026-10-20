package activation

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-primitives/canvas/internal/authoring"
	"github.com/agent-primitives/canvas/internal/canvas"
)

type testRuntime struct {
	seals, closes int
	refuse        bool
	sealFailure   func() error
}

func (r *testRuntime) RegisterAPIRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/state", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"runtime":true}`)) })
}
func (r *testRuntime) Metadata() map[string]any { return map[string]any{"providerCalls": 0} }
func (r *testRuntime) Seal(_ string, save func(canvas.Archive) error) error {
	if r.refuse {
		return errors.New("busy")
	}
	if err := save(canvas.Archive{State: json.RawMessage(`{"history":["accepted"]}`), Revision: "old-head", Documents: map[string]canvas.ArchivedDocument{"draft.json": {Content: `{"accepted":true}`, Digest: "sha256:old"}}}); err != nil {
		return err
	}
	r.seals++
	if r.sealFailure != nil {
		return r.sealFailure()
	}
	return nil
}
func (r *testRuntime) Close() { r.closes++ }

func packageFixture(t *testing.T) (*authoring.Packages, authoring.Package, string) {
	t.Helper()
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	draft := filepath.Join(t.TempDir(), "draft")
	if _, err := authoring.Init(root, draft, "single-review", "fresh"); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "manager")
	p, err := authoring.OpenPackages(filepath.Join(state, "packages"))
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := p.Prepare(draft, "canvas.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return p, candidate, state
}
func request(id, current, key string) ActivationRequest {
	return ActivationRequest{Candidate: id, ExpectedCurrent: current, RequestKey: key, Confirmed: true, Actor: "local person (unverified)"}
}

func TestActivationFreshLinkedHistory(t *testing.T) {
	p, candidate, root := packageFixture(t)
	var runtimes []*testRuntime
	f := Factory{Preflight: func(i Instance, p authoring.Package, path string) (any, error) {
		if p.DefinitionDigest == "" || path == "" {
			t.Fatal("no frozen identity")
		}
		return i.ID, nil
	}, Start: func(i Instance, v any) (Runtime, error) {
		if v != i.ID {
			t.Fatal("prepared instance swapped")
		}
		r := &testRuntime{}
		runtimes = append(runtimes, r)
		return r, nil
	}}
	m, err := Open(root, p, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtimes) != 0 {
		t.Fatal("opening inert catalog started runtime")
	}
	first, err := m.Activate(request(candidate.ID, "", "first-key-0000001"))
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := m.Activate(request(candidate.ID, "", "first-key-0000001"))
	if err != nil || duplicate.ID != first.ID || len(runtimes) != 1 {
		t.Fatalf("duplicate started runtime: %+v %v", duplicate, err)
	}
	second, err := m.Activate(request(candidate.ID, first.ID, "second-key-000001"))
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID || second.Previous != first.ID || runtimes[0].seals != 1 || runtimes[0].closes != 1 {
		t.Fatal("not a fresh linked retirement")
	}
	old, archive, err := m.History(first.ID)
	if err != nil || old.Next != second.ID || old.Status != "read-only" || archive.Revision != "old-head" || archive.Documents["draft.json"].Content != `{"accepted":true}` {
		t.Fatalf("archive lost: %+v %+v %v", old, archive, err)
	}
	stale := httptest.NewRequest("GET", "http://local/api/state", nil)
	stale.Header.Set("X-Canvas-Instance", first.ID)
	response := httptest.NewRecorder()
	m.ServeRuntime(response, stale)
	if response.Code != 409 {
		t.Fatalf("stale instance served current: %d", response.Code)
	}
	m.Close()
	m, err = Open(root, p, f)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.Snapshot().Active != second.ID || len(runtimes) != 3 {
		t.Fatal("restart changed identity or missing current runtime")
	}
	old, _, err = m.History(first.ID)
	if err != nil || old.Next != second.ID {
		t.Fatal("lost reciprocal links on reopen", err)
	}
	if _, err := m.Activate(request(candidate.ID, second.ID, "first-key-0000001")); err == nil {
		t.Fatal("same key accepted changed payload")
	}
}

func TestActivationPreflightRefusalLeavesOldUsable(t *testing.T) {
	p, candidate, root := packageFixture(t)
	old := &testRuntime{}
	refuse := false
	starts := 0
	f := Factory{Preflight: func(Instance, authoring.Package, string) (any, error) {
		if refuse {
			return nil, errors.New("bad provider")
		}
		return nil, nil
	}, Start: func(Instance, any) (Runtime, error) { starts++; return old, nil }}
	m, err := Open(root, p, f)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	first, err := m.Activate(request(candidate.ID, "", "first-key-0000001"))
	if err != nil {
		t.Fatal(err)
	}
	refuse = true
	if _, err = m.Activate(request(candidate.ID, first.ID, "refused-key-00001")); !errors.Is(err, ErrRefused) {
		t.Fatalf("wrong refusal: %v", err)
	}
	if old.seals != 0 || old.closes != 0 || starts != 1 || m.Snapshot().Active != first.ID || m.Snapshot().Pending != nil {
		t.Fatal("preflight consumed old runtime")
	}
	current := httptest.NewRequest("GET", "http://local/api/state", nil)
	current.Header.Set("X-Canvas-Instance", first.ID)
	response := httptest.NewRecorder()
	m.ServeRuntime(response, current)
	if response.Code != 200 {
		t.Fatal("old runtime no longer usable")
	}
}

func TestActivationPublicationFailureKeepsPendingBase(t *testing.T) {
	p, candidate, root := packageFixture(t)
	starts := 0
	var prior []byte
	f := Factory{Preflight: func(Instance, authoring.Package, string) (any, error) { return nil, nil }, Start: func(Instance, any) (Runtime, error) {
		starts++
		if starts == 2 {
			var err error
			prior, err = os.ReadFile(filepath.Join(root, "catalog.json"))
			if err != nil {
				t.Fatal(err)
			}
			if os.Remove(filepath.Join(root, "catalog.json")) != nil || os.Mkdir(filepath.Join(root, "catalog.json"), 0700) != nil {
				t.Fatal("publication failure setup")
			}
		}
		return &testRuntime{}, nil
	}}
	m, err := Open(root, p, f)
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.Activate(request(candidate.ID, "", "first-publish-key"))
	if err != nil {
		t.Fatal(err)
	}
	req := request(candidate.ID, first.ID, "second-publish-key")
	if _, err = m.Activate(req); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	view := m.Snapshot()
	if view.Active != first.ID || view.Pending == nil || view.Pending.Request.ExpectedCurrent != view.Active {
		t.Fatal("pending base replaced before durable activation publication")
	}
	pendingID := view.Pending.Instance.ID
	m.Close()
	if os.Remove(filepath.Join(root, "catalog.json")) != nil || os.WriteFile(filepath.Join(root, "catalog.json"), prior, 0600) != nil {
		t.Fatal("restore durable starting journal")
	}
	m, err = Open(root, p, f)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if starts != 2 {
		t.Fatal("restart repeated unknown startup")
	}
	result, err := m.Activate(req)
	if err != nil || result.ID != pendingID {
		t.Fatal("lost same-key publication recovery", err)
	}
}

func TestActivationCatalogRefusesDuplicateFieldsAndBrokenLinks(t *testing.T) {
	for _, kind := range []string{"duplicate", "unknown-status", "broken-link"} {
		t.Run(kind, func(t *testing.T) {
			p, candidate, root := packageFixture(t)
			f := Factory{Preflight: func(Instance, authoring.Package, string) (any, error) { return nil, nil }, Start: func(Instance, any) (Runtime, error) { return &testRuntime{}, nil }}
			m, err := Open(root, p, f)
			if err != nil {
				t.Fatal(err)
			}
			first, err := m.Activate(request(candidate.ID, "", "first-valid-key1"))
			if err != nil {
				t.Fatal(err)
			}
			if kind == "broken-link" {
				if _, err = m.Activate(request(candidate.ID, first.ID, "second-valid-key")); err != nil {
					t.Fatal(err)
				}
				m.data.Instances[0].Next = first.ID
			}
			if kind == "unknown-status" {
				m.data.Instances[0].Status = "surprise"
			}
			raw, _ := json.Marshal(m.data)
			m.Close()
			if kind == "duplicate" {
				raw = []byte(strings.Replace(string(raw), `"active":`, `"active":"forged","active":`, 1))
			}
			if os.WriteFile(filepath.Join(root, "catalog.json"), raw, 0600) != nil {
				t.Fatal("catalog fixture")
			}
			reopened, err := Open(root, p, f)
			if err == nil {
				reopened.Close()
				t.Fatal("corrupt catalog accepted", kind)
			}
		})
	}
}

func TestActivationVisibleSealUncertaintyClosesOld(t *testing.T) {
	p, candidate, root := packageFixture(t)
	old := &testRuntime{}
	f := Factory{Preflight: func(Instance, authoring.Package, string) (any, error) { return nil, nil }, Start: func(Instance, any) (Runtime, error) { return old, nil }}
	m, err := Open(root, p, f)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	first, err := m.Activate(request(candidate.ID, "", "first-seal-key-01"))
	if err != nil {
		t.Fatal(err)
	}
	old.sealFailure = func() error {
		path := filepath.Join(m.StatePath(first.ID), "canvas")
		if os.MkdirAll(path, 0700) != nil {
			t.Fatal("marker directory")
		}
		if os.WriteFile(filepath.Join(path, "retired.json"), []byte("visible seal"), 0600) != nil {
			t.Fatal("marker")
		}
		return errors.New("directory sync uncertain")
	}
	if _, err = m.Activate(request(candidate.ID, first.ID, "uncertain-seal-key")); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	if old.closes != 1 {
		t.Fatal("visible permanent seal left a live mutable predecessor")
	}
	req := httptest.NewRequest("GET", "http://local/api/state", nil)
	req.Header.Set("X-Canvas-Instance", first.ID)
	response := httptest.NewRecorder()
	m.ServeRuntime(response, req)
	if response.Code != 409 {
		t.Fatal("sealed runtime still exposed")
	}
	if view := m.Snapshot(); view.Pending == nil || view.Instances[0].Status != "read-only" || view.Instances[0].Next != view.Pending.Instance.ID {
		t.Fatal("lost uncertain retirement links")
	}
}

func TestActivationCorruptPendingDoesNotAbandonRequest(t *testing.T) {
	p, candidate, root := packageFixture(t)
	f := Factory{Preflight: func(Instance, authoring.Package, string) (any, error) { return nil, nil }, Start: func(Instance, any) (Runtime, error) { return nil, errors.New("startup interrupted") }}
	m, err := Open(root, p, f)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	req := request(candidate.ID, "", "corrupt-pending-key")
	if _, err = m.Activate(req); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	pendingID := m.Snapshot().Pending.Instance.ID
	path, err := p.DefinitionPath(candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.WriteFile(path, []byte("corrupt"), 0644) != nil {
		t.Fatal("corruption setup")
	}
	if _, err = m.Activate(req); !errors.Is(err, ErrUncertain) {
		t.Fatalf("corruption abandoned a partially opened instance: %v", err)
	}
	if pending := m.Snapshot().Pending; pending == nil || pending.Instance.ID != pendingID {
		t.Fatal("lost pending identity")
	}
	if os.WriteFile(path, original, 0644) != nil {
		t.Fatal("restore exact bytes")
	}
	m.factory.Start = func(Instance, any) (Runtime, error) { return &testRuntime{}, nil }
	result, err := m.Activate(req)
	if err != nil || result.ID != pendingID {
		t.Fatal("exact recovery could not continue", err)
	}
}

func TestActivationPendingNeedsExplicitSameKey(t *testing.T) {
	p, candidate, root := packageFixture(t)
	attempts := 0
	fail := true
	f := Factory{Preflight: func(i Instance, _ authoring.Package, _ string) (any, error) { return i.ID, nil }, Start: func(i Instance, _ any) (Runtime, error) {
		attempts++
		if fail {
			return nil, errors.New("disk failure")
		}
		return &testRuntime{}, nil
	}}
	m, err := Open(root, p, f)
	if err != nil {
		t.Fatal(err)
	}
	req := request(candidate.ID, "", "uncertain-key-001")
	if _, err = m.Activate(req); !errors.Is(err, ErrUncertain) {
		t.Fatal("startup not fenced", err)
	}
	pending := m.Snapshot().Pending
	if pending == nil {
		t.Fatal("missing durable pending identity")
	}
	id := pending.Instance.ID
	m.Close()
	m, err = Open(root, p, f)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if attempts != 1 {
		t.Fatal("restart repeated uncertain startup")
	}
	if _, err = m.Activate(request(candidate.ID, "", "new-key-must-refuse")); !errors.Is(err, ErrConflict) {
		t.Fatal("new key replaced pending request", err)
	}
	fail = false
	instance, err := m.Activate(req)
	if err != nil || instance.ID != id || attempts != 2 {
		t.Fatal("continuation did not retain identity", err)
	}
	m.Close()
	raw, err := os.ReadFile(filepath.Join(root, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("catalog absent")
	}
}
