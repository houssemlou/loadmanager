package station

import (
	"errors"
	"math"
	"testing"
)

func ptr(v float64) *float64 { return &v }

// Typical DC fast-charge taper: flat to 50%, then dropping.
var taper = []CurvePoint{{0, 150}, {50, 150}, {80, 50}, {100, 10}}

func TestCurveAt(t *testing.T) {
	cases := []struct {
		soc, want float64
	}{
		{-5, 150}, // clamp below
		{0, 150},  // first point
		{25, 150}, // flat section
		{65, 100}, // midpoint of 50→80 (150→50)
		{80, 50},  // exact point
		{90, 30},  // midpoint of 80→100 (50→10)
		{100, 10}, // last point
		{120, 10}, // clamp above
	}
	for _, tc := range cases {
		if got := CurveAt(taper, tc.soc); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("CurveAt(%v) = %v, want %v", tc.soc, got, tc.want)
		}
	}
}

func TestDemand(t *testing.T) {
	cases := []struct {
		name     string
		max      float64
		curve    []CurvePoint
		soc, req *float64
		want     float64
	}{
		{"vehicle max only", 120, nil, nil, nil, 120},
		{"curve without SoC is ignored", 120, taper, nil, nil, 120},
		{"curve below vehicle max", 120, taper, ptr(65), nil, 100},
		{"vehicle max below curve", 80, taper, ptr(10), nil, 80},
		{"explicit request wins when lowest", 120, taper, ptr(10), ptr(40), 40},
		{"request above other limits is ignored", 120, taper, ptr(90), ptr(200), 30},
		{"zero request pauses charging", 120, nil, nil, ptr(0), 0},
		{"SoC without curve is ignored", 120, nil, ptr(99), nil, 120},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Demand(tc.max, tc.curve, tc.soc, tc.req); math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("Demand = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidateCurve(t *testing.T) {
	bad := map[string][]CurvePoint{
		"empty":            {},
		"unsorted":         {{50, 100}, {20, 100}},
		"duplicate soc":    {{50, 100}, {50, 90}},
		"soc out of range": {{0, 100}, {120, 10}},
		"negative power":   {{0, -1}},
		"NaN power":        {{0, math.NaN()}},
	}
	for name, c := range bad {
		if err := ValidateCurve(c); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: expected ErrInvalidInput, got %v", name, err)
		}
	}
	if err := ValidateCurve(taper); err != nil {
		t.Errorf("valid curve rejected: %v", err)
	}
}
