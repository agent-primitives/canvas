package substrateruntime

import (
	"context"
	"strings"
	"testing"

	"github.com/agent-primitives/canvas/internal/contract"
	"google.golang.org/grpc"
)

type countingControlConn struct {
	grpc.ClientConnInterface
	calls int
}

func (c *countingControlConn) Invoke(context.Context, string, any, any, ...grpc.CallOption) error {
	c.calls++
	return nil
}

func TestGenericFactoriesAndTemplatesRefuseBeforeControl(t *testing.T) {
	b, err := contract.LoadSuite("writer", contract.SuiteRef{Directory: "../../examples/northwind-prd/suites/writer", Artifact: "../../examples/northwind-prd/layouts/writer", Digest: "sha256:5c2230b6869821a57b2faea4db3f811fdf4be0623b71bbe676d75d7905005375", Member: "writer"})
	if err != nil {
		t.Fatal(err)
	}
	legacy, _, _ := fixtureHarness(t)
	for _, tc := range []struct {
		b contract.Bundle
		p int
	}{{b, 4}, {b, 3}, {legacy, 4}} {
		c := &countingControlConn{}
		for _, factory := range []bool{false, true} {
			f := Factory(c, "fixture")
			if factory {
				f = RetainedFactory(c, "fixture")
			}
			backend, close, err := f(tc.b, tc.p)
			if backend != nil || close != nil || err == nil || !strings.Contains(err.Error(), "local-only") {
				t.Fatalf("remote native not explicitly refused: %v", err)
			}
		}
		if c.calls != 0 {
			t.Fatal("refusal reached Control")
		}
		if _, err := TemplateName(tc.b, tc.p); err == nil || !strings.Contains(err.Error(), "local-only") {
			t.Fatalf("native template not refused: %v", err)
		}
	}
}
