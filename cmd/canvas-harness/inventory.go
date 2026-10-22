package main

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/agent-primitives/suite/core"
)

type inventory struct {
	Version int     `json:"version"`
	Entries []entry `json:"entries"`
}
type entry struct {
	Kind    string `json:"kind,omitempty"`
	Bundle  string `json:"bundle,omitempty"`
	Address string `json:"address"`
}

// Decode each variant separately: an absent field is not an empty optional field.
// In particular, v1 never accepts kind, and announcement entries never accept bundle.
func decodeInventory(data []byte) (inventory, error) {
	var wire struct {
		Version int               `json:"version"`
		Entries []json.RawMessage `json:"entries"`
	}
	if err := core.Unmarshal(data, &wire); err != nil {
		return inventory{}, fmt.Errorf("invalid inventory: %w", err)
	}
	if (wire.Version != 1 && wire.Version != 2) || len(wire.Entries) == 0 || len(wire.Entries) > 32 {
		return inventory{}, errors.New("invalid inventory version or entry count")
	}
	inv := inventory{Version: wire.Version}
	for i, raw := range wire.Entries {
		var e entry
		var err error
		if wire.Version == 1 {
			var legacy struct {
				Bundle  string `json:"bundle"`
				Address string `json:"address"`
			}
			err = core.Unmarshal(raw, &legacy)
			e.Bundle, e.Address = legacy.Bundle, legacy.Address
		} else {
			var fields map[string]json.RawMessage
			err = core.Unmarshal(raw, &fields)
			if err == nil {
				err = core.Unmarshal(fields["kind"], &e.Kind)
			}
			if err == nil {
				switch e.Kind {
				case "bundle":
					err = core.Unmarshal(raw, &e)
				case "simulated-announcement":
					var fixed struct {
						Kind    string `json:"kind"`
						Address string `json:"address"`
					}
					err = core.Unmarshal(raw, &fixed)
					e.Address = fixed.Address
				default:
					err = errors.New("unknown entry kind")
				}
			}
		}
		if err != nil {
			return inventory{}, fmt.Errorf("inventory entry %d: %w", i, err)
		}
		inv.Entries = append(inv.Entries, e)
	}
	return inv, nil
}
