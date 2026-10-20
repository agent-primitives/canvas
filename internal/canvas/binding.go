package canvas

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// PreflightDefinition checks an existing ledger's binding without changing state.
// The caller holds the producer lifecycle lock and uses the same root as
// OpenDefined. OpenDefined still validates the ledger when it opens the canvas.
func PreflightDefinition(root string, definition *CompiledDefinition) error {
	raw, err := os.ReadFile(filepath.Join(root, "attempts.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("cannot read canvas ledger")
	}
	c := Canvas{definition: definition}
	if json.Unmarshal(raw, &c.data) != nil {
		return errors.New("invalid canvas attempt ledger")
	}
	return c.bindDefinition(true)
}
