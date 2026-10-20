package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"

	"github.com/agent-primitives/canvas/internal/canvas"
	"github.com/agent-primitives/canvas/internal/modelconfig"
	"github.com/agent-primitives/canvas/internal/sessionhost"
	"github.com/agent-primitives/sessions/client"
	"github.com/agent-primitives/sessions/controller"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type engineOptions struct {
	remote          *remoteEngine
	trustedEndpoint string
	modelProfile    string
	modelBase       string
	keyFile         string
}

var errEngineOptions = errors.New("invalid provider or sessions options")

func (o engineOptions) validate(authenticated bool) error {
	bad := func(message string) error { return fmt.Errorf("%w: %s", errEngineOptions, message) }
	if (authenticated && o.trustedEndpoint != "") || ((authenticated || o.trustedEndpoint != "") && o.modelProfile != "") {
		return bad("select one embedded model profile, authenticated sessions config, or trusted-loopback sessions endpoint")
	}
	if o.modelProfile != "" && o.modelProfile != "hosted" && o.modelProfile != "offline" {
		return bad("-model-profile must be hosted or offline")
	}
	if o.modelProfile == "" && (o.keyFile != "" || o.modelBase != "") {
		return bad("model overrides require explicit -model-profile")
	}
	if o.modelProfile == "offline" && o.keyFile != "" {
		return bad("-key-file requires hosted profile")
	}
	if o.modelProfile == "hosted" && o.modelBase != "" {
		return bad("hosted endpoint is selected in model.config.local.json, not -model-base")
	}
	if o.modelBase != "" {
		u, err := url.Parse(o.modelBase)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return bad("-model-base must be an HTTP(S) URL without credentials, query or fragment")
		}
	}
	if o.trustedEndpoint != "" {
		host, rawPort, err := net.SplitHostPort(o.trustedEndpoint)
		port, e := strconv.Atoi(rawPort)
		if err != nil || e != nil || host != "127.0.0.1" || port < 1 || port > 65535 || strconv.Itoa(port) != rawPort {
			return bad("-sessions-trusted-loopback requires literal 127.0.0.1:numeric-port")
		}
	}
	return nil
}

func validateProducerState(o engineOptions, state string, explicit bool) error {
	if o.trustedEndpoint == "" && o.modelProfile == "" {
		return nil
	}
	if err := validateRemoteState("explicit-producer-mode", state, explicit); err != nil {
		return errors.New("model profile or trusted-loopback sessions requires explicit separate -state")
	}
	return nil
}

func (o engineOptions) configuredProvider(root string, definition *canvas.CompiledDefinition) (controller.ModelFunc, error) {
	if o.modelProfile == "" {
		return nil, nil
	}
	profile, err := modelconfig.Load(root, o.modelProfile)
	if err != nil {
		return nil, err
	}
	if o.keyFile != "" {
		profile, err = profile.WithKeyFile(o.keyFile)
		if err != nil {
			return nil, err
		}
	}
	bundles, err := profile.Contracts(definition.Bundles)
	if err != nil {
		return nil, err
	}
	base := o.modelBase
	if base == "" {
		base = "http://127.0.0.1:11434"
	}
	provider, err := sessionhost.ConfiguredProvider(profile, filepath.Join(root, "model.lock.json"), base, bundles)
	if err != nil {
		return nil, err
	}
	definition.Bundles = bundles
	return provider, nil
}

func (o engineOptions) dialTrusted(project string) (*client.Client, error) {
	return client.Dial(o.trustedEndpoint, client.WithProject(project), client.WithDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDisableRetry(), grpc.WithNoProxy()))
}
