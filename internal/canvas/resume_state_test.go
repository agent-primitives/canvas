package canvas

import (
	"testing"

	v1 "github.com/agent-primitives/sessions/api/genpb"
	"google.golang.org/protobuf/proto"
)

func TestResumeEvidenceRequiresTerminalSessionState(t *testing.T) {
	original := &v1.Session{Metadata: &v1.ResourceMetadata{Uid: "synthetic-session"}, ExecState: v1.ExecState_EXEC_FAILED}
	for _, test := range []struct {
		state v1.ExecState
		want  bool
	}{
		{v1.ExecState_EXEC_COMPLETED, true},
		{v1.ExecState_EXEC_FAILED, true},
		{v1.ExecState_EXEC_CANCELED, true},
		{v1.ExecState_EXEC_PENDING, false},
		{v1.ExecState_EXEC_RUNNING, false},
		{v1.ExecState_EXEC_AWAITING, false},
		{v1.ExecState(0), false},
		{v1.ExecState(999), false},
	} {
		t.Run(test.state.String(), func(t *testing.T) {
			latest := proto.Clone(original).(*v1.Session)
			latest.ExecState = test.state
			if got := sameSessionEvidence(original, latest); got != test.want {
				t.Fatalf("Resume evidence with state %s: allowed=%v want=%v", test.state, got, test.want)
			}
		})
	}
}
