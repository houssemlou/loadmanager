// Package config defines the static station topology (grid connection,
// chargers, connectors) and loads/validates it from a JSON file.
//
// The topology is fixed for the lifetime of the process: it describes
// physical hardware, which does not change at runtime. Dynamic state
// (sessions, charger availability) lives in the station package.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
)

// Connector is a single plug on a charger (e.g. one CCS cable).
type Connector struct {
	ID   string `json:"id"`
	Type string `json:"type,omitempty"` // informational, e.g. "CCS", "Type2"
	// MaxPowerKW is the connector/cable rating. Optional: 0 means
	// "same as the charger" and is defaulted on load.
	MaxPowerKW float64 `json:"max_power_kw,omitempty"`
}

// Charger is a physical EVSE. Its power electronics are shared by all of
// its connectors: the sum of connector setpoints never exceeds MaxPowerKW.
type Charger struct {
	ID         string      `json:"id"`
	MaxPowerKW float64     `json:"max_power_kw"`
	Connectors []Connector `json:"connectors"`
}

// Config is the full station description.
type Config struct {
	StationID      string  `json:"station_id"`
	GridCapacityKW float64 `json:"grid_capacity_kw"`
	// SafetyMarginKW is kept unallocated below the grid capacity
	// (e.g. for auxiliary loads or metering error). Optional, default 0.
	SafetyMarginKW float64   `json:"safety_margin_kw,omitempty"`
	Chargers       []Charger `json:"chargers"`
}

// EffectiveGridKW is the power the allocator is allowed to distribute.
func (c *Config) EffectiveGridKW() float64 {
	return c.GridCapacityKW - c.SafetyMarginKW
}

// Load reads, defaults and validates a station config file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(raw)
}

// Parse decodes, defaults and validates a station config from JSON.
// Unknown fields are rejected so typos (e.g. "max_power") fail at startup
// instead of silently becoming 0.
func Parse(raw []byte) (*Config, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	for i := range c.Chargers {
		ch := &c.Chargers[i]
		for j := range ch.Connectors {
			if ch.Connectors[j].MaxPowerKW == 0 {
				ch.Connectors[j].MaxPowerKW = ch.MaxPowerKW
			}
		}
	}
}

// Validate checks structural and physical consistency. All problems are
// reported at once (errors.Join) so an operator can fix a file in one pass.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if !positive(c.GridCapacityKW) {
		add("grid_capacity_kw must be > 0, got %v", c.GridCapacityKW)
	}
	if !finite(c.SafetyMarginKW) || c.SafetyMarginKW < 0 {
		add("safety_margin_kw must be >= 0, got %v", c.SafetyMarginKW)
	} else if positive(c.GridCapacityKW) && c.SafetyMarginKW >= c.GridCapacityKW {
		add("safety_margin_kw (%v) must be < grid_capacity_kw (%v)", c.SafetyMarginKW, c.GridCapacityKW)
	}
	if len(c.Chargers) == 0 {
		add("at least one charger is required")
	}

	chargerIDs := map[string]bool{}
	for i, ch := range c.Chargers {
		where := fmt.Sprintf("chargers[%d]", i)
		if ch.ID == "" {
			add("%s: id is required", where)
		} else if chargerIDs[ch.ID] {
			add("%s: duplicate charger id %q", where, ch.ID)
		}
		chargerIDs[ch.ID] = true

		if !positive(ch.MaxPowerKW) {
			add("%s (%s): max_power_kw must be > 0, got %v", where, ch.ID, ch.MaxPowerKW)
		}
		if len(ch.Connectors) == 0 {
			add("%s (%s): at least one connector is required", where, ch.ID)
		}

		connIDs := map[string]bool{}
		for j, cn := range ch.Connectors {
			cwhere := fmt.Sprintf("%s.connectors[%d]", where, j)
			if cn.ID == "" {
				add("%s: id is required", cwhere)
			} else if connIDs[cn.ID] {
				add("%s: duplicate connector id %q on charger %q", cwhere, cn.ID, ch.ID)
			}
			connIDs[cn.ID] = true

			if !positive(cn.MaxPowerKW) {
				add("%s (%s): max_power_kw must be > 0, got %v", cwhere, cn.ID, cn.MaxPowerKW)
			} else if cn.MaxPowerKW > ch.MaxPowerKW {
				add("%s (%s): max_power_kw (%v) exceeds charger max_power_kw (%v)",
					cwhere, cn.ID, cn.MaxPowerKW, ch.MaxPowerKW)
			}
		}
	}
	return errors.Join(errs...)
}

func finite(v float64) bool   { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func positive(v float64) bool { return finite(v) && v > 0 }
