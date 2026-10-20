package canvas

// These data-only records preserve historical ledger JSON. They do not enable
// old workspace activation, domain validation, migrations or new dispatch.
type BoardChangeDTO struct {
	Prospect string `json:"prospect"`
	OldRank  int    `json:"oldRank"`
	NewRank  int    `json:"newRank"`
	OldTier  int    `json:"oldTier"`
	NewTier  int    `json:"newTier"`
	Reason   string `json:"reason"`
}

type InjuryDTO struct {
	ID            string `json:"id"`
	Prospect      string `json:"prospect"`
	Injury        string `json:"injury"`
	RecoveryWeeks int    `json:"recoveryWeeks"`
	Source        string `json:"source"`
	Actor         string `json:"actor,omitempty"`
}

type fanoutIntent struct {
	ID       string   `json:"id"`
	Parent   string   `json:"parent"`
	ForkSeq  int64    `json:"forkSeq"`
	Attempts []string `json:"attempts"`
	Status   string   `json:"status"`
}
