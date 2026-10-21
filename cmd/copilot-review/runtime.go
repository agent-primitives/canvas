package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/agent-primitives/canvas/internal/authoring"
	"github.com/agent-primitives/canvas/internal/canvas"
	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/demostate"
	"github.com/agent-primitives/canvas/internal/sessionhost"
	"github.com/agent-primitives/canvas/internal/sharedspace"
	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/client"
	"github.com/agent-primitives/sessions/controller"
)

// preparedRuntime keeps provider credentials and compiled contracts in memory,
// never in a frozen package or activation journal.
type preparedRuntime struct {
	definition      *canvas.CompiledDefinition
	options         engineOptions
	provider        controller.ModelFunc
	calls           *atomic.Int64
	fixtureTemplate bool
}

type runtimeEngine struct {
	canvas   *canvas.Canvas
	prepared *preparedRuntime
	close    func()
	once     sync.Once
}

func prepareRuntime(root, state, definitionPath string, options engineOptions, frozen *authoring.Package) (*preparedRuntime, error) {
	if err := options.validate(options.remote != nil); err != nil {
		return nil, err
	}
	if options.flowAgentRuns > 0 && definitionPath == "" {
		return nil, errors.New("-flow-agent-runs requires an explicit -definition and separate -state")
	}
	if err := canvas.CheckWritable(state); err != nil {
		return nil, err
	}
	if err := canvas.CheckWritable(filepath.Join(state, "canvas")); err != nil {
		return nil, err
	}
	remote := options.remote
	selectedDefinition := definitionPath
	if selectedDefinition == "" {
		selectedDefinition = filepath.Join(root, "examples", "northwind-prd", "canvas.yaml")
	} else if !filepath.IsAbs(selectedDefinition) {
		selectedDefinition = filepath.Join(root, selectedDefinition)
	}
	var definition *canvas.CompiledDefinition
	var e error
	if frozen != nil {
		if remote != nil || options.trustedEndpoint != "" {
			return nil, errEngineOptions
		}
		definition, e = canvas.LoadGenericDefinition(selectedDefinition)
		if e != nil {
			return nil, authoring.ErrPackage
		}
		mode := "manual"
		if len(definition.Flows) > 0 {
			mode = "flow"
		}
		if definition.Digest != frozen.DefinitionDigest || frozen.RuntimeMode != mode {
			return nil, authoring.ErrPackage
		}
		if mode == "flow" && options.flowAgentRuns == 0 || mode == "manual" && options.flowAgentRuns > 0 {
			return nil, canvas.ErrFlowRuntime
		}
	} else {
		load := canvas.LoadManualDefinitionWithResolver
		if options.flowAgentRuns > 0 {
			load = canvas.LoadGenericDefinitionWithResolver
		}
		definition, e = load(selectedDefinition, contract.NewReleaseResolver(filepath.Join(state, "suite-cache")))
		if e != nil {
			return nil, e
		}
		if options.flowAgentRuns > 0 && len(definition.Flows) == 0 {
			return nil, canvas.ErrFlowRuntime
		}
	}
	// Refuse before state, providers or sessions RPCs: the human credential is
	// never relabelled as the execution owner.
	if remote != nil && !remote.hasExecutionBinding() {
		return nil, errExecutionBinding
	}
	selected, e := options.configuredProvider(root, definition)
	if e != nil {
		return nil, e
	}
	fixtureTemplate := frozen != nil && selected == nil && !qualifiedNorthwindFixture(definition, frozen)
	if fixtureTemplate {
		// Reuse the already qualified generated-template fixture, not the
		// Northwind-specific provider for arbitrary authored document IDs.
		selected, e = authoring.NewFixtureProvider(selectedDefinition, definition)
		if e != nil {
			return nil, authoring.ErrPackage
		}
	}
	var calls atomic.Int64
	provider := func(_ context.Context, req api.ModelRequest) (api.ModelResponse, error) {
		calls.Add(1)
		var input struct {
			Edit *struct {
				Document string `json:"document"`
			} `json:"edit"`
			Mode     string            `json:"mode"`
			Agent    string            `json:"agent"`
			Request  string            `json:"request"`
			Evidence map[string]string `json:"evidence"`
		}
		if len(req.Messages) == 0 || json.Unmarshal([]byte(req.Messages[len(req.Messages)-1].Text()), &input) != nil || input.Mode != "documents" {
			return api.ModelResponse{}, errors.New("demo accepts document requests only")
		}
		changes := []map[string]string{}
		switch input.Agent {
		case "writer":
			changes = append(changes, map[string]string{"document": "prd", "operation": "replace", "content": strings.Replace(definition.Seed["prd.json"], "Fictional Northwind PRD", "Proposed fictional Northwind pilot", 1)}, map[string]string{"document": "announcement", "operation": "replace", "content": "# Northwind pilot\n\nInvite three fictional reviewers to a local preview.\nNothing is sent or published.\n"})
		case "reviewer":
			changes = append(changes, map[string]string{"document": "review", "operation": "replace", "content": definition.Seed["review.json"]})
		default:
			return api.ModelResponse{}, errors.New("unsupported demo agent")
		}
		if input.Edit != nil {
			var err error
			changes, err = fixtureEditChanges(definition, input.Edit.Document, input.Evidence)
			if err != nil {
				return api.ModelResponse{}, err
			}
		} else if options.flowAgentRuns > 0 {
			var err error
			changes, err = flowFixtureChanges(definition, input.Agent, input.Request, input.Evidence)
			if err != nil {
				return api.ModelResponse{}, err
			}
		}
		raw, e := json.Marshal(map[string]any{"changes": changes})
		if e != nil {
			return api.ModelResponse{}, e
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", string(raw))}, nil
	}
	if selected != nil {
		provider = func(ctx context.Context, req api.ModelRequest) (api.ModelResponse, error) {
			calls.Add(1)
			return selected(ctx, req)
		}
	}
	return &preparedRuntime{definition: definition, options: options, provider: provider, calls: &calls, fixtureTemplate: fixtureTemplate}, nil
}

// openRuntime owns the existing Sessions, SharedSpace and Canvas lifecycle, but
// no HTTP listener. The prepared definition/provider are consumed without reload.
func openRuntime(state string, prepared *preparedRuntime) (_ *runtimeEngine, err error) {
	definition, options, provider := prepared.definition, prepared.options, prepared.provider
	remote := options.remote
	if err = canvas.CheckWritable(state); err != nil {
		return nil, err
	}
	if err = canvas.CheckWritable(filepath.Join(state, "canvas")); err != nil {
		return nil, err
	}
	release, e := demostate.Acquire(state)
	if e != nil {
		return nil, e
	}
	cleanups := []func(){release}
	cleanup := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	var cl, execution *client.Client
	var sessionsAddress string
	var archives *contract.ArchiveResolver
	if remote == nil && options.trustedEndpoint == "" {
		h, e := sessionhost.Start(filepath.Join(state, "sessions"), definition.Bundles, provider)
		if e != nil {
			return nil, e
		}
		cleanups = append(cleanups, func() { h.Close() })
		sessionsAddress = h.Address()
		archives = &contract.ArchiveResolver{Directory: filepath.Join(state, "sessions", "contracts")}
		cl, e = client.Dial(sessionsAddress, client.WithProject(definition.ID))
	} else if remote != nil {
		sessionsAddress = remote.endpoint
		cl, e = remote.dial(definition.ID)
	} else {
		sessionsAddress = options.trustedEndpoint
		cl, e = options.dialTrusted(definition.ID)
	}
	if e != nil {
		return nil, e
	}
	cleanups = append(cleanups, func() { cl.Close() })
	if remote != nil {
		// Same endpoint and TLS trust, separate service credential. Registered
		// before OpenDefined so Canvas.Close runs before either connection closes.
		if execution, e = remote.dialExecution(definition.ID); e != nil {
			return nil, e
		}
		cleanups = append(cleanups, func() { execution.Close() })
	}
	space, e := sharedspace.OpenWithPolicy(filepath.Join(state, "space"), definition.Seed, definition.Policy)
	if e != nil {
		return nil, e
	}
	cleanups = append(cleanups, func() { space.Close() })
	var c *canvas.Canvas
	if options.flowAgentRuns > 0 {
		c, e = canvas.OpenFlowRuntime(filepath.Join(state, "canvas"), space, cl, sessionsAddress, definition, contract.LocalBinder{}, canvas.FlowRuntimeOptions{MaxAgentRuns: options.flowAgentRuns}, archives)
	} else {
		c, e = canvas.OpenDefined(filepath.Join(state, "canvas"), space, cl, sessionsAddress, definition, contract.LocalBinder{}, archives)
	}
	if e != nil {
		return nil, e
	}
	cleanups = append(cleanups, func() { c.Close() })
	if remote != nil {
		c.EnableAuthentication(remote.reviewPolicy)
		if e = c.ConfigureExecutionService(canvas.ExecutionService{Client: execution, Credentials: remote.executionAuth, Identity: remote.executionIdentity}); e != nil {
			return nil, e
		}
	} else if options.trustedEndpoint != "" {
		c.EnableRemoteSessions()
	}
	return &runtimeEngine{canvas: c, prepared: prepared, close: cleanup}, nil
}

func (e *runtimeEngine) Close() { e.once.Do(e.close) }

// Metadata returns a fresh public projection; private provider configuration
// remains in the prepared handle.
func (e *runtimeEngine) Metadata() map[string]any {
	options, calls := e.prepared.options, e.prepared.calls
	if options.remote != nil {
		return map[string]any{"mode": "authenticated-sessions", "identity": "engine-verified per action; sessions authorizes", "headlessPrefix": "/engine", "reviewerRole": options.remote.reviewRole}
	}
	info := map[string]any{"mode": "deterministic-northwind", "identity": "unauthenticated (prototype)", "providerCalls": calls.Load(), "realModelCalls": 0, "headlessPrefix": "/engine"}
	if e.prepared.fixtureTemplate {
		info["mode"] = "deterministic-template"
	}
	if options.modelProfile != "" {
		info = map[string]any{"mode": options.modelProfile + "-model", "identity": "not exercised (local unauthenticated engine)", "providerInvocations": calls.Load(), "completionTokenCeiling": 4096, "headlessPrefix": "/engine"}
	} else if options.trustedEndpoint != "" {
		info = map[string]any{"mode": "trusted-loopback-sessions", "identity": "not exercised (unauthenticated trusted connection)", "modelCalls": nil, "executorCalls": nil, "headlessPrefix": "/engine"}
	}
	if options.flowAgentRuns > 0 {
		info["flowRuntime"], info["requesterRole"] = true, "GM"
	}
	return info
}
