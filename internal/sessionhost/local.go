package sessionhost

import (
	"context"
	"errors"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/modelpin"
	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
)

// PinnedOllama gives embedded and standalone hosts the same offline provider.
// It checks the pinned model manifest at startup and before every call.
func PinnedOllama(lockPath, base string, bundles []contract.Bundle) (controller.ModelFunc, error) {
	if len(bundles) == 0 {
		return nil, errors.New("model provider requires bundles")
	}
	lock, err := modelpin.Load(lockPath)
	if err != nil {
		return nil, err
	}
	if err := modelpin.Check(context.Background(), base, lock.Model, lock.ManifestDigest); err != nil {
		return nil, err
	}
	for _, b := range bundles {
		if b.Model != lock.Model {
			return nil, errors.New("bundle model differs from local model lock")
		}
	}
	provider, err := Ollama(base, bundles[0].Model)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, request api.ModelRequest) (api.ModelResponse, error) {
		if err := modelpin.Check(ctx, base, lock.Model, lock.ManifestDigest); err != nil {
			return api.ModelResponse{}, err
		}
		return provider(ctx, request)
	}, nil
}
