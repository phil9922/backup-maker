// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"path/filepath"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
)

// TestExampleConfig keeps config.toml.example in lockstep with the schema:
// it must parse cleanly and validate with all commented samples enabled.
func TestExampleConfig(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "config.toml.example"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := New()
	if err := toml.Unmarshal(data, cfg); err != nil {
		t.Fatalf("example does not parse: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("example does not validate: %v", err)
	}

	// The example must not smuggle a real password field into a target.
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if targets, ok := raw["target"].([]any); ok {
		for _, tr := range targets {
			if m, ok := tr.(map[string]any); ok {
				if _, has := m["password"]; has {
					t.Error("example config must never contain a password field")
				}
			}
		}
	}
}
