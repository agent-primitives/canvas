package canvas

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sharedspace"
	"github.com/agent-primitives/sessions/client"
)

// Removing the runtime project override must break actual Sessions routing,
// without changing the source definition or the model's definition identity.
func TestDefinedInstanceProjectsAndReopen(t *testing.T) {
	base, d, calls := definedFixture(t, nil)
	original, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, instance := range []string{"release-one", "release-two", d.ID} {
		t.Run(instance, func(t *testing.T) {
			root := t.TempDir()
			space, err := sharedspace.OpenWithPolicy(filepath.Join(root, "space"), d.Seed, d.Policy)
			if err != nil {
				t.Fatal(err)
			}
			defer space.Close()
			cl, err := client.Dial(base.address, client.WithProject(instance))
			if err != nil {
				t.Fatal(err)
			}
			defer cl.Close()
			stateRoot := filepath.Join(root, "canvas")
			c, err := OpenDefinedInstance(stateRoot, space, cl, base.address, d, instance, contract.LocalBinder{})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			id, err := c.Manual("writer", "", "Draft fictional requirements.", "GM")
			if err != nil {
				t.Fatal(err)
			}
			run := completed(t, c, id)
			if run.Status != "proposed" {
				t.Fatalf("instance execution: %+v", run)
			}
			session, err := cl.GetSession(context.Background(), run.Session)
			if err != nil || session.GetMetadata().GetProject() != instance || session.GetLabels()["canvas"] != instance {
				t.Fatalf("actual Sessions project: %v %v", session, err)
			}
			var input struct{ Definition struct{ ID, Digest string } }
			if json.Unmarshal([]byte(c.data.Attempts[0].Input), &input) != nil || input.Definition.ID != d.ID || input.Definition.Digest != d.Digest {
				t.Fatal("runtime identity leaked into source definition input")
			}
			state, err := c.State()
			if err != nil || state.Definition.ID != d.ID || state.Definition.Digest != d.Digest {
				t.Fatalf("source identity: %+v %v", state.Definition, err)
			}
			c.Close()
			before := calls.Load()
			raw, err := os.ReadFile(filepath.Join(stateRoot, "attempts.json"))
			if err != nil {
				t.Fatal(err)
			}
			var marker map[string]json.RawMessage
			if json.Unmarshal(raw, &marker) != nil || string(marker["sessionProject"]) != `"`+instance+`"` {
				t.Fatalf("missing instance marker: %s", raw)
			}
			for _, wrong := range []string{"", "another-release"} {
				var rejected *Canvas
				if wrong == "" {
					rejected, err = OpenDefined(stateRoot, space, cl, base.address, d, contract.LocalBinder{})
				} else {
					rejected, err = OpenDefinedInstance(stateRoot, space, cl, base.address, d, wrong, contract.LocalBinder{})
				}
				if err == nil {
					rejected.Close()
					t.Fatalf("accepted incompatible marker %q", wrong)
				}
				after, readErr := os.ReadFile(filepath.Join(stateRoot, "attempts.json"))
				if readErr != nil || !bytes.Equal(raw, after) || calls.Load() != before {
					t.Fatal("rejected open changed state or dispatched")
				}
			}
			reopened, err := OpenDefinedInstance(stateRoot, space, cl, base.address, d, instance, contract.LocalBinder{})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			saved, ok := reopened.GetRun(id)
			if !ok || saved.Session != run.Session || saved.Status != "proposed" || calls.Load() != before {
				t.Fatal("same-instance reopen lost state or dispatched")
			}
		})
	}
	after, err := json.Marshal(d)
	if err != nil || !reflect.DeepEqual(original, after) || d.runtimeProject != "" {
		t.Fatal("constructor mutated original compiled definition")
	}
	legacySessions, err := base.client.ListSessions(context.Background(), d.ID)
	if err != nil || len(legacySessions) != 1 {
		t.Fatalf("distinct instances leaked into definition project: %d %v", len(legacySessions), err)
	}
}

func TestDefinedInstanceDoesNotAdoptLegacyState(t *testing.T) {
	c, d, calls := definedFixture(t, nil)
	c.Close()
	filename := filepath.Join(c.root, "attempts.json")
	before, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(before, []byte(`"sessionProject"`)) {
		t.Fatal("default state gained marker")
	}
	for _, instance := range []string{"hosted-instance", d.ID} {
		opened, err := OpenDefinedInstance(c.root, c.space, c.client, c.address, d, instance, c.binder)
		if err == nil {
			opened.Close()
			t.Fatalf("adopted unmarked legacy state as %q", instance)
		}
	}
	reopened, err := OpenDefined(c.root, c.space, c.client, c.address, d, c.binder)
	if err != nil {
		t.Fatal(err)
	}
	reopened.Close()
	after, err := os.ReadFile(filename)
	if err != nil || !bytes.Equal(before, after) || calls.Load() != 0 {
		t.Fatal("default reopen changed legacy JSON bytes or dispatched")
	}
}

func TestDefinedInstanceRejectsInvalidNamesAndFlowRuntime(t *testing.T) {
	d := nativeData(t)
	for _, id := range []string{"", " ", "Upper", "../escape", "a_b", "1name", strings.Repeat("a", 64)} {
		root := filepath.Join(t.TempDir(), "must-not-exist")
		c, err := OpenDefinedInstance(root, nil, nil, "", d, id, contract.LocalBinder{})
		if err == nil {
			c.Close()
			t.Fatalf("accepted invalid instance %q", id)
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatal("invalid instance created state")
		}
	}
	c, err := OpenDefinedInstance(filepath.Join(t.TempDir(), "nil"), nil, nil, "", nil, "valid", contract.LocalBinder{})
	if err == nil {
		c.Close()
		t.Fatal("accepted nil definition")
	}
	flow := *d
	flow.flows = map[string]compiledFlow{"unsupported": {}}
	c, err = OpenDefinedInstance(filepath.Join(t.TempDir(), "flow"), nil, nil, "", &flow, "valid", contract.LocalBinder{})
	if err == nil {
		c.Close()
		t.Fatal("hosted API bypassed manual-runtime check")
	}
}
