package canvas

// ForkAncestry is a read-only view of Canvas's recorded run mappings. It does
// not query Sessions, reconstruct missing identity or authorize another turn.
type ForkAncestry struct {
	Run      string       `json:"run"`
	Session  string       `json:"session,omitempty"`
	Parent   ForkParent   `json:"parent"`
	Document ForkDocument `json:"document"`
	Children []ForkChild  `json:"children"`
}

type ForkParent struct {
	Available bool   `json:"available"`
	Session   string `json:"session,omitempty"`
	ForkSeq   int64  `json:"forkSeq,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// ForkDocument identifies the recorded document snapshot, not the parent
// session checkpoint. A branch name is not recorded and is never inferred.
type ForkDocument struct {
	Available bool   `json:"available"`
	Base      string `json:"base,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type ForkChild struct {
	Run       string       `json:"run"`
	Session   string       `json:"session,omitempty"`
	ForkSeq   int64        `json:"forkSeq,omitempty"`
	Status    string       `json:"status"`
	Available bool         `json:"available"`
	Reason    string       `json:"reason,omitempty"`
	Document  ForkDocument `json:"document"`
}

// Forks reads only the recorded Session/ParentSession/ForkSeq/Base values and
// child statuses under the ledger lock. Missing evidence stays unavailable.
func (c *Canvas) Forks(id string) (ForkAncestry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var run *Run
	for i := range c.data.Attempts {
		if id != "" && c.data.Attempts[i].ID == id {
			run = &c.data.Attempts[i].Run
			break
		}
	}
	if run == nil {
		return ForkAncestry{}, errNotFound
	}
	view := ForkAncestry{
		Run: run.ID, Session: run.Session, Document: forkDocument(run.Base),
		Parent:   ForkParent{Session: run.ParentSession, ForkSeq: run.ForkSeq},
		Children: []ForkChild{},
	}
	view.Parent.Available = run.Session != "" && run.ParentSession != "" && run.ForkSeq > 0
	if !view.Parent.Available {
		view.Parent.Reason = "Parent session ancestry is unavailable or incomplete in the recorded run."
	}
	if run.Session == "" {
		return view, nil
	}
	for _, child := range c.data.Attempts {
		if child.ParentSession != run.Session {
			continue
		}
		entry := ForkChild{
			Run: child.ID, Session: child.Session, ForkSeq: child.ForkSeq,
			Status: child.Status, Document: forkDocument(child.Base),
			Available: child.Session != "" && child.ForkSeq > 0,
		}
		if !entry.Available {
			entry.Reason = "Child session ancestry is unavailable or incomplete in the recorded run."
		}
		view.Children = append(view.Children, entry)
	}
	return view, nil
}

func forkDocument(base string) ForkDocument {
	view := ForkDocument{Available: base != "", Base: base}
	if !view.Available {
		view.Reason = "Document base is unavailable in the recorded run."
	}
	return view
}
