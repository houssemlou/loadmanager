package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validJSON = `{
  "station_id": "test",
  "grid_capacity_kw": 400,
  "chargers": [
    {"id": "CP1", "max_power_kw": 300, "connectors": [
      {"id": "1", "type": "CCS"},
      {"id": "2", "type": "CCS", "max_power_kw": 200}
    ]}
  ]
}`

func TestParse_ValidConfigAppliesConnectorDefault(t *testing.T) {
	cfg, err := Parse([]byte(validJSON))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	conns := cfg.Chargers[0].Connectors
	if conns[0].MaxPowerKW != 300 {
		t.Errorf("connector 1 should default to charger max 300, got %v", conns[0].MaxPowerKW)
	}
	if conns[1].MaxPowerKW != 200 {
		t.Errorf("connector 2 explicit max should be kept, got %v", conns[1].MaxPowerKW)
	}
	if got := cfg.EffectiveGridKW(); got != 400 {
		t.Errorf("effective grid = %v, want 400", got)
	}
}

func TestParse_Rejects(t *testing.T) {
	cases := []struct {
		name    string
		json    string
		wantErr string
	}{
		{
			name:    "unknown field (typo)",
			json:    `{"grid_capacity": 400, "chargers": []}`,
			wantErr: "unknown field",
		},
		{
			name:    "non-positive grid",
			json:    `{"grid_capacity_kw": 0, "chargers": [{"id":"A","max_power_kw":50,"connectors":[{"id":"1"}]}]}`,
			wantErr: "grid_capacity_kw must be > 0",
		},
		{
			name:    "margin >= grid",
			json:    `{"grid_capacity_kw": 100, "safety_margin_kw": 100, "chargers": [{"id":"A","max_power_kw":50,"connectors":[{"id":"1"}]}]}`,
			wantErr: "must be < grid_capacity_kw",
		},
		{
			name:    "negative margin",
			json:    `{"grid_capacity_kw": 100, "safety_margin_kw": -1, "chargers": [{"id":"A","max_power_kw":50,"connectors":[{"id":"1"}]}]}`,
			wantErr: "safety_margin_kw must be >= 0",
		},
		{
			name:    "no chargers",
			json:    `{"grid_capacity_kw": 100, "chargers": []}`,
			wantErr: "at least one charger",
		},
		{
			name:    "duplicate charger id",
			json:    `{"grid_capacity_kw": 100, "chargers": [{"id":"A","max_power_kw":50,"connectors":[{"id":"1"}]},{"id":"A","max_power_kw":50,"connectors":[{"id":"1"}]}]}`,
			wantErr: "duplicate charger id",
		},
		{
			name:    "charger without connectors",
			json:    `{"grid_capacity_kw": 100, "chargers": [{"id":"A","max_power_kw":50,"connectors":[]}]}`,
			wantErr: "at least one connector",
		},
		{
			name:    "duplicate connector id",
			json:    `{"grid_capacity_kw": 100, "chargers": [{"id":"A","max_power_kw":50,"connectors":[{"id":"1"},{"id":"1"}]}]}`,
			wantErr: "duplicate connector id",
		},
		{
			name:    "connector above charger max",
			json:    `{"grid_capacity_kw": 100, "chargers": [{"id":"A","max_power_kw":50,"connectors":[{"id":"1","max_power_kw":60}]}]}`,
			wantErr: "exceeds charger max_power_kw",
		},
		{
			name:    "non-positive charger max",
			json:    `{"grid_capacity_kw": 100, "chargers": [{"id":"A","max_power_kw":0,"connectors":[{"id":"1","max_power_kw":10}]}]}`,
			wantErr: "max_power_kw must be > 0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.json))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoad_ShippedConfigsAreValid(t *testing.T) {
	files, err := filepath.Glob("../../configs/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no shipped configs found: %v", err)
	}
	for _, f := range files {
		if _, err := Load(f); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err == nil || !os.IsNotExist(unwrapAll(err)) {
		t.Fatalf("expected not-exist error, got %v", err)
	}
}

func unwrapAll(err error) error {
	for {
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		err = u.Unwrap()
	}
}
