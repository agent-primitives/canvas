// Package activation owns local package selection and runtime lifetime, not flow execution.
package activation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/agent-primitives/canvas/internal/authoring"
	"github.com/agent-primitives/canvas/internal/canvas"
	"github.com/agent-primitives/canvas/internal/demostate"
)

var (
	ErrConflict   = errors.New("activation conflicts with the inspected instance or request")
	ErrRefused    = errors.New("activation refused; check the frozen package, provider configuration and settled predecessor")
	ErrUncertain  = errors.New("activation is pending; inspect it and explicitly continue the exact request")
	idPattern     = regexp.MustCompile(`^[a-f0-9]{20}$`)
	keyPattern    = regexp.MustCompile(`^[a-zA-Z0-9_-]{16,128}$`)
	digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type Instance struct {
	ID               string `json:"id"`
	Package          string `json:"package"`
	DefinitionDigest string `json:"definitionDigest"`
	RuntimeMode      string `json:"runtimeMode"`
	Previous         string `json:"previous,omitempty"`
	Next             string `json:"next,omitempty"`
	Status           string `json:"status"`
	ArchiveDigest    string `json:"archiveDigest,omitempty"`
}
type ActivationRequest struct {
	Candidate       string `json:"candidate"`
	ExpectedCurrent string `json:"expectedCurrent"`
	RequestKey      string `json:"requestKey"`
	Confirmed       bool   `json:"confirmed"`
	Actor           string `json:"actor"`
}
type Runtime interface {
	RegisterAPIRoutes(*http.ServeMux)
	Metadata() map[string]any
	Seal(string, func(canvas.Archive) error) error
	Close()
}
type Factory struct {
	Preflight func(Instance, authoring.Package, string) (any, error)
	Start     func(Instance, any) (Runtime, error)
}
type activationRecord struct {
	Request         ActivationRequest `json:"request"`
	Instance        string            `json:"instance"`
	Phase           string            `json:"phase"`
	PreviousArchive string            `json:"previousArchive,omitempty"`
}
type catalog struct {
	Version   int                          `json:"version"`
	Active    string                       `json:"active"`
	Candidate string                       `json:"candidate,omitempty"`
	Pending   string                       `json:"pending,omitempty"`
	Instances []Instance                   `json:"instances"`
	Requests  map[string]*activationRecord `json:"requests"`
}
type Pending struct {
	Request  ActivationRequest `json:"request"`
	Instance Instance          `json:"instance"`
	Phase    string            `json:"phase"`
}
type View struct {
	Active    string     `json:"active"`
	Candidate string     `json:"candidate,omitempty"`
	Instances []Instance `json:"instances"`
	Pending   *Pending   `json:"pending,omitempty"`
}
type Manager struct {
	mu       sync.RWMutex
	root     string
	packages *authoring.Packages
	factory  Factory
	data     catalog
	runtime  Runtime
	routes   *http.ServeMux
	release  func()
	boot     string
	closed   bool
}

// Open starts only an already committed instance. Pending activation is never retried here.
func Open(root string, p *authoring.Packages, f Factory) (*Manager, error) {
	if p == nil || f.Preflight == nil || f.Start == nil {
		return nil, ErrRefused
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, ErrRefused
	}
	release, err := demostate.Acquire(root)
	if err != nil {
		return nil, ErrRefused
	}
	m := &Manager{root: root, packages: p, factory: f, release: release, boot: canvas.ID(), data: catalog{Version: 1, Instances: []Instance{}, Requests: map[string]*activationRecord{}}}
	fail := func() (*Manager, error) { release(); return nil, ErrRefused }
	_, err = os.Lstat(filepath.Join(root, "catalog.json"))
	if err == nil {
		raw, readErr := m.read("catalog.json", 4<<20)
		if readErr != nil || decode(raw, &m.data, 4<<20) != nil || m.validate() != nil {
			return fail()
		}
	} else if !os.IsNotExist(err) {
		return fail()
	}
	if m.data.Active != "" {
		active := m.instance(m.data.Active)
		if active == nil {
			return fail()
		}
		// A durable seal can precede the catalog publication that describes it.
		sealed := canvas.CheckWritable(filepath.Join(m.StatePath(active.ID), "canvas")) != nil
		if sealed {
			pending := m.data.Requests[m.data.Pending]
			if pending == nil || pending.Request.ExpectedCurrent != active.ID || pending.PreviousArchive == "" {
				return fail()
			}
			active.Status, active.Next, active.ArchiveDigest = "read-only", pending.Instance, pending.PreviousArchive
		} else if m.data.Pending == "" || m.data.Requests[m.data.Pending].Phase == "preflight" || m.data.Requests[m.data.Pending].Phase == "prepared" || m.data.Requests[m.data.Pending].Phase == "sealing" {
			pkg, err := p.Load(active.Package)
			if err != nil || m.checkOwner(*active) != nil {
				return fail()
			}
			path, err := p.DefinitionPath(pkg.ID)
			if err != nil {
				return fail()
			}
			prepared, err := f.Preflight(*active, pkg, path)
			if err != nil {
				return fail()
			}
			r, err := f.Start(*active, prepared)
			if err != nil {
				return fail()
			}
			m.install(r)
		}
	}
	return m, nil
}
func (m *Manager) instance(id string) *Instance {
	for i := range m.data.Instances {
		if m.data.Instances[i].ID == id {
			return &m.data.Instances[i]
		}
	}
	return nil
}
func (m *Manager) StatePath(id string) string {
	if !idPattern.MatchString(id) {
		return ""
	}
	return filepath.Join(m.root, "instances", id, "runtime")
}
func (m *Manager) install(r Runtime) {
	m.runtime = r
	m.routes = http.NewServeMux()
	r.RegisterAPIRoutes(m.routes)
}
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	if m.runtime != nil {
		m.runtime.Close()
		m.runtime = nil
	}
	m.release()
}

func (m *Manager) Snapshot() View {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v := View{Active: m.data.Active, Candidate: m.data.Candidate, Instances: slices.Clone(m.data.Instances)}
	if r := m.data.Requests[m.data.Pending]; r != nil {
		v.Pending = &Pending{r.Request, *m.instance(r.Instance), r.Phase}
	}
	return v
}
func (m *Manager) Prepare(root, definition string) (authoring.Package, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return authoring.Package{}, ErrRefused
	}
	p, err := m.packages.Prepare(root, definition)
	if err != nil {
		return p, ErrRefused
	}
	m.data.Candidate = p.ID
	if err = m.save(); err != nil {
		return p, ErrUncertain
	}
	return p, nil
}
func (m *Manager) Metadata() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	info := map[string]any{"mode": "inert-authoring", "identity": "local unverified attribution (prototype)", "providerCalls": 0, "realModelCalls": 0}
	if m.runtime != nil {
		info = m.runtime.Metadata()
	}
	info["boot"], info["authoring"], info["headlessPrefix"] = m.boot+":"+m.data.Active, true, "/engine"
	info["activeInstance"] = ""
	if m.runtime != nil {
		info["activeInstance"] = m.data.Active
	}
	if pending := m.data.Requests[m.data.Pending]; pending != nil {
		// A restarted shell may have no live runtime. Bind explicit continuation
		// to this durable request, not to a fictitious live predecessor.
		info["pendingActivation"] = struct {
			ActivationRequest
			Instance string `json:"instance"`
		}{pending.Request, pending.Instance}
	}
	return info
}
func (m *Manager) ServeRuntime(w http.ResponseWriter, r *http.Request) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed || m.runtime == nil || r.Header.Get("X-Canvas-Instance") != m.data.Active {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(409)
		io.WriteString(w, `{"code":"INSTANCE_CHANGED","error":"Inspect or reopen the exact active instance; archived instances are read-only."}`)
		return
	}
	m.routes.ServeHTTP(w, r)
}
func (m *Manager) History(id string) (Instance, canvas.Archive, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	i := m.instance(id)
	if i == nil || i.Status != "read-only" || !digestPattern.MatchString(i.ArchiveDigest) {
		return Instance{}, canvas.Archive{}, ErrRefused
	}
	raw, err := m.read("archives/"+i.ArchiveDigest+".json", 64<<20)
	if err != nil || sum(raw) != i.ArchiveDigest {
		return Instance{}, canvas.Archive{}, ErrRefused
	}
	var archive canvas.Archive
	if decode(raw, &archive, 64<<20) != nil {
		return Instance{}, canvas.Archive{}, ErrRefused
	}
	return *i, archive, nil
}

func validRequest(r ActivationRequest) bool {
	return r.Confirmed && digestPattern.MatchString(r.Candidate) && (r.ExpectedCurrent == "" || idPattern.MatchString(r.ExpectedCurrent)) && keyPattern.MatchString(r.RequestKey) && strings.TrimSpace(r.Actor) != "" && len(r.Actor) <= 100
}
func (m *Manager) Activate(req ActivationRequest) (Instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || !validRequest(req) {
		return Instance{}, ErrRefused
	}
	record := m.data.Requests[req.RequestKey]
	if record != nil {
		if record.Request != req {
			return Instance{}, ErrConflict
		}
		if record.Phase == "active" {
			return *m.instance(record.Instance), nil
		}
		if record.Phase == "refused" {
			return Instance{}, ErrRefused
		}
		if m.data.Pending != req.RequestKey {
			return Instance{}, ErrConflict
		}
	} else {
		if m.data.Pending != "" || req.ExpectedCurrent != m.data.Active {
			return Instance{}, ErrConflict
		}
		pkg, err := m.packages.Load(req.Candidate)
		if err != nil {
			return Instance{}, ErrRefused
		}
		instance := Instance{ID: canvas.ID(), Package: pkg.ID, DefinitionDigest: pkg.DefinitionDigest, RuntimeMode: pkg.RuntimeMode, Previous: m.data.Active, Status: "pending"}
		m.data.Instances = append(m.data.Instances, instance)
		record = &activationRecord{Request: req, Instance: instance.ID, Phase: "preflight"}
		m.data.Requests[req.RequestKey] = record
		m.data.Pending = req.RequestKey
		if m.save() != nil {
			return instance, ErrUncertain
		}
	}
	instance := *m.instance(record.Instance)
	pkg, err := m.packages.Load(instance.Package)
	if err != nil {
		return m.preflightFailure(record, instance)
	}
	path, err := m.packages.DefinitionPath(pkg.ID)
	if err != nil {
		return m.preflightFailure(record, instance)
	}
	if record.Phase != "preflight" && m.checkOwner(instance) != nil {
		return instance, ErrUncertain
	}
	prepared, err := m.factory.Preflight(instance, pkg, path)
	if err != nil {
		if record.Phase == "preflight" {
			return m.refuse(record, instance)
		}
		return instance, ErrUncertain
	}
	if record.Phase == "preflight" {
		if m.ensureOwner(instance) != nil {
			return m.refuse(record, instance)
		}
		record.Phase = "prepared"
		if m.save() != nil {
			return instance, ErrUncertain
		}
	}
	if instance.Previous != "" {
		old := m.instance(instance.Previous)
		if old == nil {
			return instance, ErrUncertain
		}
		if old.Status != "read-only" {
			if m.runtime == nil {
				return instance, ErrUncertain
			}
			record.Phase = "sealing"
			if m.save() != nil {
				return instance, ErrUncertain
			}
			err = m.runtime.Seal(instance.ID, func(archive canvas.Archive) error {
				raw, e := json.Marshal(archive)
				if e != nil || len(raw) > 64<<20 {
					return ErrRefused
				}
				hash := sum(raw)
				name := "archives/" + hash + ".json"
				if _, statErr := os.Lstat(filepath.Join(m.root, filepath.FromSlash(name))); statErr == nil {
					prior, readErr := m.read(name, 64<<20)
					if readErr != nil || !bytes.Equal(prior, raw) {
						return ErrRefused
					}
				} else if !os.IsNotExist(statErr) {
					return ErrRefused
				} else if e = m.write(name, raw); e != nil {
					return e
				}
				record.PreviousArchive = hash
				return m.save()
			})
			if err != nil {
				if canvas.CheckWritable(filepath.Join(m.StatePath(old.ID), "canvas")) == nil {
					return m.refuse(record, instance)
				}
				// A visible seal may precede a failed directory sync. It is never
				// safe to keep serving the old writer while reporting uncertainty.
				m.runtime.Close()
				m.runtime, m.routes = nil, nil
				old.Status, old.Next, old.ArchiveDigest = "read-only", instance.ID, record.PreviousArchive
				return instance, ErrUncertain
			}
			m.runtime.Close()
			m.runtime = nil
			m.routes = nil
			old.Status, old.Next, old.ArchiveDigest = "read-only", instance.ID, record.PreviousArchive
		}
		if old.Next != instance.ID || record.PreviousArchive == "" {
			return instance, ErrUncertain
		}
	}
	record.Phase = "starting"
	if m.save() != nil {
		return instance, ErrUncertain
	}
	runtime, err := m.factory.Start(instance, prepared)
	if err != nil {
		return instance, ErrUncertain
	}
	current := m.instance(instance.ID)
	current.Status = "active"
	m.data.Active, m.data.Pending, record.Phase = current.ID, "", "active"
	if m.save() != nil {
		runtime.Close()
		m.data.Active = instance.Previous
		m.data.Pending = req.RequestKey
		record.Phase = "starting"
		current.Status = "pending"
		return instance, ErrUncertain
	}
	m.install(runtime)
	return *current, nil
}
func (m *Manager) preflightFailure(r *activationRecord, i Instance) (Instance, error) {
	if r.Phase == "preflight" || r.Phase == "prepared" {
		return m.refuse(r, i)
	}
	// After startup or a possibly durable seal, retain the reserved identity even
	// if the operator must restore corrupt package bytes before continuing.
	return i, ErrUncertain
}
func (m *Manager) refuse(r *activationRecord, i Instance) (Instance, error) {
	r.Phase = "refused"
	m.data.Pending = ""
	m.instance(i.ID).Status = "refused"
	if m.save() != nil {
		return i, ErrUncertain
	}
	return i, ErrRefused
}

type owner struct{ ID, Package, DefinitionDigest, RuntimeMode string }

func ownership(i Instance) owner { return owner{i.ID, i.Package, i.DefinitionDigest, i.RuntimeMode} }
func (m *Manager) ensureOwner(i Instance) error {
	relative := "instances/" + i.ID + "/owner.json"
	if _, err := os.Lstat(filepath.Join(m.root, filepath.FromSlash(relative))); err == nil {
		return m.checkOwner(i)
	} else if !os.IsNotExist(err) {
		return ErrRefused
	}
	if _, err := os.Lstat(filepath.Join(m.root, "instances", i.ID)); !os.IsNotExist(err) {
		return ErrRefused
	}
	raw, _ := json.Marshal(ownership(i))
	return m.write(relative, raw)
}
func (m *Manager) checkOwner(i Instance) error {
	raw, err := m.read("instances/"+i.ID+"/owner.json", 4096)
	if err != nil {
		return err
	}
	var o owner
	if decode(raw, &o, 4096) != nil || o != ownership(i) {
		return ErrRefused
	}
	return nil
}
func (m *Manager) validate() error {
	if m.data.Version != 1 || m.data.Requests == nil || len(m.data.Instances) > 4096 {
		return ErrRefused
	}
	ids := map[string]bool{}
	for _, i := range m.data.Instances {
		if !idPattern.MatchString(i.ID) || ids[i.ID] || !digestPattern.MatchString(i.Package) || i.DefinitionDigest == "" || (i.RuntimeMode != "manual" && i.RuntimeMode != "flow") {
			return ErrRefused
		}
		ids[i.ID] = true
	}
	for _, i := range m.data.Instances {
		switch i.Status {
		case "pending", "active", "read-only", "refused":
		default:
			return ErrRefused
		}
		if i.Previous != "" && !ids[i.Previous] || i.Next != "" && !ids[i.Next] {
			return ErrRefused
		}
		if i.Status == "read-only" {
			if !digestPattern.MatchString(i.ArchiveDigest) || i.Next == "" || m.instance(i.Next).Previous != i.ID {
				return ErrRefused
			}
		}
		if i.Previous != "" && i.Status == "active" && m.instance(i.Previous).Next != i.ID {
			return ErrRefused
		}
	}
	if m.data.Active != "" && !ids[m.data.Active] {
		return ErrRefused
	}
	if m.data.Candidate != "" && !digestPattern.MatchString(m.data.Candidate) {
		return ErrRefused
	}
	for key, r := range m.data.Requests {
		if r == nil || key != r.Request.RequestKey || !validRequest(r.Request) || !ids[r.Instance] {
			return ErrRefused
		}
		i := m.instance(r.Instance)
		if r.Request.Candidate != i.Package || r.Request.ExpectedCurrent != i.Previous {
			return ErrRefused
		}
		switch r.Phase {
		case "preflight", "prepared", "sealing", "starting", "active", "refused":
		default:
			return ErrRefused
		}
	}
	if m.data.Pending != "" && m.data.Requests[m.data.Pending] == nil {
		return ErrRefused
	}
	return nil
}
func (m *Manager) save() error {
	raw, err := json.Marshal(m.data)
	if err != nil || len(raw) > 4<<20 {
		return ErrRefused
	}
	return m.write("catalog.json", raw)
}
func sum(raw []byte) string { v := sha256.Sum256(raw); return hex.EncodeToString(v[:]) }
func decode(raw []byte, v any, limit int) error {
	if len(raw) > limit {
		return ErrRefused
	}
	tokens := json.NewDecoder(bytes.NewReader(raw))
	if uniqueJSON(tokens) != nil {
		return ErrRefused
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return ErrRefused
	}
	var extra any
	if !errors.Is(d.Decode(&extra), io.EOF) {
		return ErrRefused
	}
	return nil
}
func uniqueJSON(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return ErrRefused
	}
	kind, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	if kind == '{' {
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			name, ok := key.(string)
			if err != nil || !ok || seen[name] {
				return ErrRefused
			}
			seen[name] = true
			if uniqueJSON(d) != nil {
				return ErrRefused
			}
		}
	} else if kind == '[' {
		for d.More() {
			if uniqueJSON(d) != nil {
				return ErrRefused
			}
		}
	} else {
		return ErrRefused
	}
	_, err = d.Token()
	return err
}
func (m *Manager) read(name string, limit int) ([]byte, error) {
	root, err := os.OpenRoot(m.root)
	if err != nil {
		return nil, ErrRefused
	}
	defer root.Close()
	if safeParents(root, name) != nil {
		return nil, ErrRefused
	}
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return nil, ErrRefused
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, ErrRefused
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, ErrRefused
	}
	raw, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil || len(raw) > limit {
		return nil, ErrRefused
	}
	return raw, nil
}
func safeParents(root *os.Root, name string) error {
	components := strings.Split(filepath.ToSlash(filepath.Dir(name)), "/")
	path := ""
	for _, p := range components {
		if p == "." {
			continue
		}
		path = filepath.Join(path, p)
		info, err := root.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrRefused
		}
	}
	return nil
}
func (m *Manager) write(name string, raw []byte) error {
	root, err := os.OpenRoot(m.root)
	if err != nil {
		return ErrRefused
	}
	defer root.Close()
	if err = root.MkdirAll(filepath.Dir(name), 0700); err != nil || safeParents(root, name) != nil {
		return ErrRefused
	}
	tmp := name + ".tmp-" + canvas.ID()
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return ErrRefused
	}
	defer root.Remove(tmp)
	_, writeErr := f.Write(raw)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return ErrRefused
	}
	if root.Rename(tmp, name) != nil {
		return ErrUncertain
	}
	dir, err := root.Open(filepath.Dir(name))
	if err != nil {
		return ErrUncertain
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return ErrUncertain
	}
	return nil
}
