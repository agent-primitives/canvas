package sharedspace

import (
	"fmt"
	"testing"
)

// One UI poll reads proposals and history. With many open proposals this used
// to start a git process per proposal document on every poll.
func BenchmarkPoll(b *testing.B) {
	s, err := Open(b.TempDir(), map[string]string{"board.md": "board\n", "strategy.md": "strategy\n"})
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	base, err := s.Head()
	if err != nil {
		b.Fatal(err)
	}
	for i := range 40 {
		changes := map[string]string{"board.md": fmt.Sprintf("board %d\n", i), fmt.Sprintf("prospects/p%d.md", i): "dossier\n"}
		if _, err := s.Propose(base, changes, Attribution{Agent: "scouting"}); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for range b.N {
		if _, err := s.Proposals(); err != nil {
			b.Fatal(err)
		}
		if _, err := s.History(); err != nil {
			b.Fatal(err)
		}
	}
}
