package sessionhost

import (
	"context"
	"errors"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/modelconfig"
	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
)

// An inherited fork must not send its recorded model to a different provider.
// This fixed message is safe to store in the journal.
var ErrModelProfileMismatch = errors.New("recorded model differs from selected profile; select the matching profile or regenerate on latest")

func ConfiguredProvider(profile modelconfig.Profile, lockPath, offlineBase string, bundles []contract.Bundle) (controller.ModelFunc, error) {
	if len(bundles) == 0 {
		return nil, errors.New("model provider requires execution contracts")
	}
	if profile.Model == "" {
		return nil, errors.New("model profile has no model")
	}
	for _, b := range bundles {
		if b.Model != profile.Model {
			return nil, ErrModelProfileMismatch
		}
	}
	var provider controller.ModelFunc
	var err error
	switch profile.Name {
	case "hosted":
		h := profile.Hosted
		if h.Model != profile.Model {
			return nil, ErrModelProfileMismatch
		}
		if h.KeyFile == "" {
			return nil, errors.New("hosted: no key file configured")
		}
		provider, err = compatible(endpoint{label: "hosted", base: h.Endpoint, model: h.Model, keyFile: h.KeyFile, auth: h.Auth, https: true, reasoning: true})
	case "offline":
		if profile.Model != modelconfig.OfflineModel {
			return nil, ErrModelProfileMismatch
		}
		provider, err = PinnedOllama(lockPath, offlineBase, bundles)
	default:
		return nil, errors.New("unsupported model profile; no automatic fallback")
	}
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, req api.ModelRequest) (api.ModelResponse, error) {
		if req.Model != profile.Model {
			return api.ModelResponse{}, ErrModelProfileMismatch
		}
		return provider(ctx, req)
	}, nil
}
