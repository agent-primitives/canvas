// Package sessionhost runs the Sessions service: the journal, the runtime and
// one harness per archived agent contract. Canvas talks to it over gRPC.
package sessionhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"sync"

	"github.com/agent-primitives/canvas/internal/announcement"
	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/placement"
	"github.com/agent-primitives/sessions/runtime/local"
	"github.com/agent-primitives/sessions/session"
	"github.com/agent-primitives/sessions/sqlitelog"
	"google.golang.org/grpc"
)

// Host holds the listener, backends and journal. Start and StartAt only listen
// on 127.0.0.1; a caller-supplied listener must be kept private by the caller.
type Host struct {
	address   string
	server    *grpc.Server
	store     *sqlitelog.Store
	closers   []func() error
	harnesses map[string]bundleHarness
	closeOnce sync.Once
	closeErr  error
	served    chan error
}

func Start(root string, bundles []contract.Bundle, provider controller.ModelFunc) (*Host, error) {
	return StartAt(root, "127.0.0.1:0", bundles, provider)
}

// StartAt listens on a fixed local address. The Sessions API has no
// authentication, so only 127.0.0.1 is allowed.
func StartAt(root, address string, bundles []contract.Bundle, provider controller.ModelFunc) (*Host, error) {
	host, port, err := net.SplitHostPort(address)
	n, portErr := strconv.Atoi(port)
	if err != nil || host != "127.0.0.1" || portErr != nil || n < 0 || n > 65535 {
		return nil, errors.New("sessionhost: listener must bind 127.0.0.1 with a numeric port")
	}
	lis, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("sessionhost: cannot listen on %s: %w", address, err)
	}
	return StartWithOptions(root, bundles, provider, Options{Listener: lis})
}

// Options explicitly transfers ownership of a listener and each factory closer
// to the host, including on startup failure. A nil factory uses the local runtime.
// Factory errors must be safe to display; startup preserves their error context.
type Options struct {
	Listener net.Listener
	// Trusted assembly may supply TLS and auth interceptors; the default local demo remains unchanged.
	ServerOptions  []grpc.ServerOption
	BackendFactory func(contract.Bundle, int) (placement.Backend, func() error, error)
}

func StartWithOptions(root string, bundles []contract.Bundle, provider controller.ModelFunc, opts Options) (*Host, error) {
	if opts.Listener == nil {
		return nil, errors.New("sessionhost: explicit listener required")
	}
	started := false
	defer func() {
		if !started {
			opts.Listener.Close()
		}
	}()
	if root == "" || provider == nil {
		return nil, errors.New("sessionhost: root and provider required")
	}
	root, err := resolveDir(root)
	if err != nil {
		return nil, err
	}
	// Parse exact source bytes before touching disk; supplied parsed fields cannot
	// silently disagree with the bytes whose digest names the immutable harness.
	for _, b := range bundles {
		parsed, err := contract.Parse(b.Source)
		if err != nil || !sameBundle(parsed, b) {
			return nil, errors.New("sessionhost: invalid bundle identity")
		}
	}
	archived, err := loadContracts(root, bundles)
	if err != nil {
		return nil, err
	}
	if len(archived) == 0 {
		return nil, errors.New("sessionhost: at least one contract required")
	}
	if err := privateJournal(filepath.Join(root, "journal.db")); err != nil {
		return nil, err
	}
	store, err := sqlitelog.Open(filepath.Join(root, "journal.db"))
	if err != nil {
		return nil, errors.New("sessionhost: cannot open journal")
	}
	h := &Host{store: store, harnesses: map[string]bundleHarness{}}
	placers := map[string]*placement.Placer{}
	var names []string
	for _, b := range archived {
		for _, profile := range contract.Profiles(b) {
			name := contract.HarnessName(b.Digest, profile)
			har := bundleHarness{bundle: b, profile: profile}
			h.harnesses[name] = har
			var backend placement.Backend
			var closeBackend func() error
			if opts.BackendFactory == nil {
				b := local.New(har)
				backend, closeBackend = b, b.Close
			} else {
				backend, closeBackend, err = opts.BackendFactory(b, profile)
			}
			if closeBackend != nil {
				h.closers = append(h.closers, closeBackend)
			}
			if err != nil || backend == nil {
				h.Close()
				if err != nil {
					return nil, fmt.Errorf("sessionhost: cannot configure backend: %w", err)
				}
				return nil, errors.New("sessionhost: cannot configure backend")
			}
			desc, describeErr := backend.Describe(context.Background())
			want, _ := har.Describe(context.Background())
			if describeErr != nil || desc.ID != want.ID || len(desc.Models) != 1 || desc.Models[0] != b.Model || desc.Capabilities != want.Capabilities {
				h.Close()
				return nil, errors.New("sessionhost: backend descriptor mismatch")
			}
			placers[name] = placement.New(backend, safeProvider(provider))
			names = append(names, name)
		}
	}
	// The deterministic simulation is native/local only, never a model bundle
	// or a default candidate. Remote factories serve only their bundle contracts.
	if opts.BackendFactory == nil {
		backend := local.New(announcementHarness{})
		h.closers = append(h.closers, backend.Close)
		placers[announcement.HarnessName] = placement.New(backend, refuseAnnouncementModel, placement.WithToolExecutor(executeAnnouncement))
	}
	sort.Strings(names)
	defaultBundle := h.harnesses[names[0]].bundle
	if len(bundles) > 0 {
		defaultBundle = bundles[0]
	}
	defaultHarness := contract.HarnessName(defaultBundle.Digest, contract.Profile(defaultBundle))
	registry, err := placement.NewRegistry(defaultHarness, placers)
	if err != nil {
		h.Close()
		return nil, errors.New("sessionhost: cannot configure harnesses")
	}
	lis := opts.Listener
	h.address = lis.Addr().String()
	h.server = grpc.NewServer(append([]grpc.ServerOption{grpc.WaitForHandlers(true)}, opts.ServerOptions...)...)
	v1.RegisterSessionsServer(h.server, session.NewService(store, registry))
	registerDiagnostics(h.server, h)
	h.served = make(chan error, 1)
	go func() { h.served <- h.server.Serve(lis) }()
	started = true
	return h, nil
}

func (h *Host) Address() string { return h.address }

// Done receives the gRPC server's result once it stops serving. A value
// before Close means the server died; after Close it is usually nil.
func (h *Host) Done() <-chan error { return h.served }

func (h *Host) Close() error {
	h.closeOnce.Do(func() {
		if h.server != nil {
			h.server.Stop()
		}
		for _, closeBackend := range h.closers {
			h.closeErr = errors.Join(h.closeErr, closeBackend())
		}
		if h.store != nil {
			h.closeErr = errors.Join(h.closeErr, h.store.Close())
		}
	})
	return h.closeErr
}

func sameBundle(a, b contract.Bundle) bool {
	// Legacy YAML never persisted AuthoredDigest; native archives derive it.
	return a.ID == b.ID && a.Digest == b.Digest && (a.Suite == nil || a.AuthoredDigest == b.AuthoredDigest) && a.Instructions == b.Instructions && a.Model == b.Model && bytes.Equal(a.Source, b.Source) && reflect.DeepEqual(a.Suite, b.Suite)
}

func loadContracts(root string, current []contract.Bundle) ([]contract.Bundle, error) {
	dir := filepath.Join(root, "contracts")
	if err := privateContractDirectory(root, dir); err != nil {
		return nil, errors.New("sessionhost: cannot create private contract archive")
	}
	if err := syncContractDirectory(root); err != nil {
		return nil, errors.New("sessionhost: cannot persist contract archive directory")
	}
	byDigest := map[string]contract.Bundle{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, errors.New("sessionhost: cannot read contract archive")
	}
	for _, entry := range entries {
		// Only this reserved directory is unpublished. Digest directories,
		// including incomplete ones, may serve old sessions and must validate.
		if entry.Name() == ".staging" && entry.IsDir() {
			continue
		}
		if !entry.IsDir() {
			return nil, errors.New("sessionhost: invalid contract archive entry")
		}
		b, err := contract.Load(filepath.Join(dir, entry.Name(), "agent.yaml"))
		if err != nil || b.Digest != entry.Name() {
			return nil, errors.New("sessionhost: archived contract digest mismatch")
		}
		if err := os.Chmod(filepath.Join(dir, entry.Name(), "agent.yaml"), 0600); err != nil {
			return nil, errors.New("sessionhost: cannot secure archived contract")
		}
		byDigest[b.Digest] = b
	}
	for _, b := range current {
		if prior, ok := byDigest[b.Digest]; ok {
			if !sameBundle(prior, b) {
				return nil, errors.New("sessionhost: immutable contract mismatch")
			}
			continue
		}
		if err := publishContract(dir, b); err != nil {
			return nil, err
		}
		// Start already validated the fields; retain our own exact source bytes.
		b.Source = bytes.Clone(b.Source)
		byDigest[b.Digest] = b
	}
	var out []contract.Bundle
	for _, b := range byDigest {
		out = append(out, b)
	}
	return out, nil
}

func publishContract(dir string, b contract.Bundle) error {
	staging := filepath.Join(dir, ".staging")
	if err := privateContractDirectory(dir, staging); err != nil {
		return errors.New("sessionhost: cannot create private contract staging")
	}
	target := filepath.Join(dir, b.Digest)
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		return errors.New("sessionhost: contract archive target already exists or is unsafe")
	}
	stage, err := os.MkdirTemp(staging, b.Digest+"-")
	if err != nil {
		return errors.New("sessionhost: cannot stage contract")
	}
	// Remove only our own unpublished directory; crash leftovers are ignored.
	defer os.RemoveAll(stage)
	f, err := os.OpenFile(filepath.Join(stage, "agent.yaml"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("sessionhost: cannot stage contract")
	}
	n, writeErr := f.Write(b.Source)
	syncErr := f.Sync()
	closeErr := f.Close()
	if n != len(b.Source) || writeErr != nil || syncErr != nil || closeErr != nil {
		return errors.New("sessionhost: cannot persist contract")
	}
	if err := syncContractDirectory(stage); err != nil {
		return errors.New("sessionhost: cannot persist staged contract directory")
	}
	// No-replace is required even if another publisher creates an empty
	// directory or symlink after the check.
	if err := renameNoReplace(stage, target); err != nil {
		return errors.New("sessionhost: cannot publish immutable contract")
	}
	if err := errors.Join(syncContractDirectory(dir), syncContractDirectory(staging)); err != nil {
		return errors.New("sessionhost: cannot persist contract publication")
	}
	return nil
}

// resolveDir returns dir with symlinks in its existing ancestors resolved, so
// system links such as /home -> var/home work. dir itself belongs to the host
// and must not be a symlink.
func resolveDir(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", errors.New("sessionhost: invalid state path")
	}
	if fi, err := os.Lstat(dir); err == nil && !fi.IsDir() {
		return "", fmt.Errorf("sessionhost: %s must be a real directory, not a file or symlink", dir)
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("sessionhost: cannot inspect %s", dir)
	}
	missing := []string{filepath.Base(dir)}
	for parent := filepath.Dir(dir); ; parent = filepath.Dir(parent) {
		resolved, err := filepath.EvalSymlinks(parent)
		if err == nil {
			return filepath.Join(append([]string{resolved}, missing...)...), nil
		}
		if !os.IsNotExist(err) || filepath.Dir(parent) == parent {
			return "", fmt.Errorf("sessionhost: cannot resolve %s", parent)
		}
		missing = append([]string{filepath.Base(parent)}, missing...)
	}
}

// privateContractDirectory creates path below root. root and every existing
// component between them must be real directories, so MkdirAll cannot follow
// a symlink out of the host's state.
func privateContractDirectory(root, path string) error {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		fi, err := os.Lstat(p)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && !fi.IsDir() {
			return errors.New("sessionhost: contract archive path must be a directory")
		}
		if p == root || filepath.Dir(p) == p {
			break
		}
	}
	return os.MkdirAll(path, 0700)
}

func syncContractDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

func privateJournal(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		return f.Close()
	}
	if !os.IsExist(err) {
		return errors.New("sessionhost: cannot create private journal")
	}
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return errors.New("sessionhost: journal must be a regular file")
	}
	if err := os.Chmod(path, 0600); err != nil {
		return errors.New("sessionhost: cannot secure journal")
	}
	return nil
}

func safeProvider(fn controller.ModelFunc) controller.ModelFunc {
	return func(ctx context.Context, req api.ModelRequest) (api.ModelResponse, error) {
		resp, err := fn(ctx, req)
		if err == nil {
			return resp, nil
		}
		// Upstream stores harness error strings in ERROR events. Never forward a
		// provider's response body, key, URL or captured completion into that channel.
		if ctx.Err() != nil {
			return api.ModelResponse{}, ctx.Err()
		}
		if errors.Is(err, ErrModelProfileMismatch) {
			return api.ModelResponse{}, ErrModelProfileMismatch
		}
		return api.ModelResponse{}, errors.New("model provider failed")
	}
}

// NewBundleHarness uses the exact archived contract and an explicit supported
// profile. Callers selecting the family default use contract.Profile(b).
func NewBundleHarness(b contract.Bundle, profile int) (api.Harness, error) {
	allowed := false
	for _, p := range contract.Profiles(b) {
		allowed = allowed || p == profile
	}
	parsed, err := contract.Parse(b.Source)
	if err != nil || !sameBundle(parsed, b) || !allowed {
		return nil, errors.New("sessionhost: invalid bundle identity or profile")
	}
	return bundleHarness{bundle: parsed, profile: profile}, nil
}

// BundleDescriptor describes supported remote actor bindings, not local-only suites.
func BundleDescriptor(b contract.Bundle, profile int) (api.Descriptor, error) {
	if b.Suite != nil || profile == 4 {
		return api.Descriptor{}, errors.New("sessionhost: native suite/profile 4 is local-only; remote actors unsupported")
	}
	h, err := NewBundleHarness(b, profile)
	if err != nil {
		return api.Descriptor{}, err
	}
	return h.Describe(context.Background())
}

type bundleHarness struct {
	bundle  contract.Bundle
	profile int
}

func (h bundleHarness) Describe(context.Context) (api.Descriptor, error) {
	return api.Descriptor{ID: contract.HarnessName(h.bundle.Digest, h.profile), Models: []string{h.bundle.Model}, Capabilities: api.Capabilities{Resumability: api.ResumabilityStatelessReplay, ForkSafe: true}}, nil
}

const outputInstructions = "\n\n/no_think\nReturn only one compact JSON object, without markdown or thinking text. The current journaled input specifies the mode. For scout or scouting use {\"dossier\":string,\"rank\":int,\"tier\":int,\"rationale\":string}; for injury use {\"rank\":int,\"tier\":int,\"rationale\":string}; for scenario use {\"strategy\":string}. Keep the response short."

const questionInstructions = "\n\nFor question mode only, return {\"answer\":string} with a nonempty answer of at most 3000 characters. Give a read-only explanation of the recorded recommendation, not a new assessment or proposal. Distinguish recorded facts, the historical accepted snapshot, unaccepted recommendations, and the selected proposal's status at question time. Do not assume the historical snapshot is still current. Do not invent facts or reasoning absent from the recorded evidence; state uncertainty when the evidence does not explain the decision. Do not call tools or claim tool use. Other modes retain their existing output requirements."

func (h bundleHarness) Run(ctx context.Context, start *api.Start, sink api.EventSink) error {
	instructions := h.bundle.Instructions + outputInstructions
	var genericFormat string
	if h.profile == 4 {
		var err error
		genericFormat, err = profile4ResponseFormat(start.Inputs)
		if err != nil {
			return err
		}
		instructions = h.bundle.Instructions + genericInstructions
	}
	if h.profile == 3 {
		instructions += questionInstructions
	}
	messages := []api.Message{*api.TextMessage("system", instructions)}
	for _, ev := range start.History {
		if ev.Message != nil && (ev.Kind == api.EventInput || ev.Kind == api.EventOutput) {
			messages = append(messages, *ev.Message)
		}
	}
	messages = append(messages, start.Inputs...)
	params := map[string]string{"max_tokens": "700", "temperature": "0.2"}
	if h.profile == 2 {
		params["response_format"] = responseFormat(h.bundle.ID, start.Inputs)
	} else if h.profile == 3 {
		params["response_format"] = profile3ResponseFormat(h.bundle.ID, start.Inputs)
	} else if h.profile == 4 {
		params["response_format"] = genericFormat
		params["max_tokens"] = "16384"
	}
	_, err := sink.Model(ctx, api.ModelRequest{Model: h.bundle.Model, Messages: messages, Params: params})
	return err
}
