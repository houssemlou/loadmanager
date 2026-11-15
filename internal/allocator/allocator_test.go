package allocator

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
)

// key is a short helper: k("CP1","1").
func k(ch, conn string) SessionKey { return SessionKey{ChargerID: ch, ConnectorID: conn} }

// briefStation builds the station from the assignment brief:
// 400 kW grid, 2 chargers of 300 kW, 2 connectors each.
// caps maps "CP1/1" style ids to session caps; absent = no session.
func briefStation(grid float64, caps map[string]float64) Input {
	in := Input{GridKW: grid}
	for _, ch := range []string{"CP1", "CP2"} {
		c := Charger{ID: ch, MaxKW: 300}
		for _, conn := range []string{"1", "2"} {
			if v, ok := caps[ch+"/"+conn]; ok {
				c.Sessions = append(c.Sessions, Session{Key: k(ch, conn), CapKW: v})
			}
		}
		in.Chargers = append(in.Chargers, c)
	}
	return in
}

func TestAllocate_Scenarios(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		want map[SessionKey]float64
		why  string
	}{
		{
			name: "single vehicle takes all it can (capped by charger)",
			in:   briefStation(400, map[string]float64{"CP1/1": 350}),
			want: map[SessionKey]float64{k("CP1", "1"): 300},
			why:  "brief: a lone car takes all the power, but never more than its charger",
		},
		{
			name: "second vehicle arrives on other charger: equal share",
			in:   briefStation(400, map[string]float64{"CP1/1": 350, "CP2/1": 350}),
			want: map[SessionKey]float64{k("CP1", "1"): 200, k("CP2", "1"): 200},
			why:  "brief: power is shared when a second car arrives",
		},
		{
			name: "first vehicle leaves: second takes all it can",
			in:   briefStation(400, map[string]float64{"CP2/1": 350}),
			want: map[SessionKey]float64{k("CP2", "1"): 300},
			why:  "brief: remaining car gets the freed power",
		},
		{
			name: "vehicle below its fair share: surplus redistributed",
			in:   briefStation(400, map[string]float64{"CP1/1": 350, "CP2/1": 100}),
			want: map[SessionKey]float64{k("CP1", "1"): 300, k("CP2", "1"): 100},
			why:  "water-filling: unused share is not wasted",
		},
		{
			name: "charger bottleneck: fairness per session, leftover goes elsewhere",
			in:   briefStation(600, map[string]float64{"CP1/1": 300, "CP1/2": 300, "CP2/1": 300}),
			want: map[SessionKey]float64{k("CP1", "1"): 150, k("CP1", "2"): 150, k("CP2", "1"): 300},
			why:  "charger cap is shared by its connectors; grid leftover flows to the other charger",
		},
		{
			name: "all four connectors busy: 100 each",
			in: briefStation(400, map[string]float64{
				"CP1/1": 300, "CP1/2": 300, "CP2/1": 300, "CP2/2": 300,
			}),
			want: map[SessionKey]float64{
				k("CP1", "1"): 100, k("CP1", "2"): 100, k("CP2", "1"): 100, k("CP2", "2"): 100,
			},
			why: "full station: grid is the only binding constraint",
		},
		{
			name: "grid above total demand: everyone gets exactly their demand",
			in:   briefStation(400, map[string]float64{"CP1/1": 50, "CP2/1": 80}),
			want: map[SessionKey]float64{k("CP1", "1"): 50, k("CP2", "1"): 80},
			why:  "never allocate more than a vehicle asks for",
		},
		{
			name: "zero-demand session gets 0 and does not consume a share",
			in:   briefStation(400, map[string]float64{"CP1/1": 0, "CP2/1": 350}),
			want: map[SessionKey]float64{k("CP1", "1"): 0, k("CP2", "1"): 300},
			why:  "a car at 100% SoC (or paused) must not steal power from others",
		},
		{
			name: "no sessions",
			in:   briefStation(400, nil),
			want: map[SessionKey]float64{},
			why:  "empty station must not divide by zero",
		},
		{
			name: "safety margin (passed as reduced grid budget) is respected",
			in:   briefStation(400-40, map[string]float64{"CP1/1": 300, "CP2/1": 300}),
			want: map[SessionKey]float64{k("CP1", "1"): 180, k("CP2", "1"): 180},
			why:  "budget is grid minus margin",
		},
		{
			name: "rounding never overshoots: 400/3",
			in:   briefStation(400, map[string]float64{"CP1/1": 300, "CP1/2": 300, "CP2/1": 300}),
			want: map[SessionKey]float64{k("CP1", "1"): 133.3, k("CP1", "2"): 133.3, k("CP2", "1"): 133.3},
			why:  "floor rounding keeps the sum ≤ grid (399.9 ≤ 400)",
		},
		{
			name: "three-level cascade: vehicle cap, then charger cap, then grid",
			in: briefStation(500, map[string]float64{
				"CP1/1": 40,  // vehicle-limited
				"CP1/2": 300, // charger-limited
				"CP2/1": 300, // grid-limited
				"CP2/2": 300, // grid-limited
			}),
			want: map[SessionKey]float64{
				k("CP1", "1"): 40, k("CP1", "2"): 160, k("CP2", "1"): 150, k("CP2", "2"): 150,
			},
			why: "exercises every freeze rule in one allocation",
		},
		{
			name: "negative / NaN caps are treated as zero",
			in:   briefStation(400, map[string]float64{"CP1/1": -10, "CP2/1": math.NaN()}),
			want: map[SessionKey]float64{k("CP1", "1"): 0, k("CP2", "1"): 0},
			why:  "defensive: bad inputs must never produce negative setpoints",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Allocate(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d setpoints %v, want %d %v (%s)", len(got), got, len(tc.want), tc.want, tc.why)
			}
			for key, w := range tc.want {
				if g, ok := got[key]; !ok || math.Abs(g-w) > 1e-9 {
					t.Errorf("%v: got %v, want %v (%s)", key, g, w, tc.why)
				}
			}
			assertInvariants(t, tc.in, got)
		})
	}
}

// TestAllocate_Properties checks the invariants hold on random stations, not just the examples.
func TestAllocate_Properties(t *testing.T) {
	rng := rand.New(rand.NewPCG(42, 2026)) // fixed seed: reproducible
	for i := range 1000 {
		in := randomInput(rng)
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			got := Allocate(in)
			assertInvariants(t, in, got)
		})
	}
}

func randomInput(rng *rand.Rand) Input {
	in := Input{GridKW: float64(rng.IntN(800)) + rng.Float64()}
	nChargers := 1 + rng.IntN(6)
	for c := range nChargers {
		ch := Charger{ID: fmt.Sprintf("C%d", c), MaxKW: float64(20 + rng.IntN(350))}
		nConn := rng.IntN(4) // 0..3 sessions
		for s := range nConn {
			ch.Sessions = append(ch.Sessions, Session{
				Key:   k(ch.ID, fmt.Sprint(s)),
				CapKW: float64(rng.IntN(400)) * rng.Float64(),
			})
		}
		in.Chargers = append(in.Chargers, ch)
	}
	return in
}

// assertInvariants verifies safety (no limit exceeded) and optimality
// (max-min fairness) of an allocation, with tolerance for 0.1 kW rounding.
func assertInvariants(t *testing.T, in Input, got Result) {
	t.Helper()
	const tiny = 1e-6
	nSessions := 0
	for _, ch := range in.Chargers {
		nSessions += len(ch.Sessions)
	}
	// Max total rounding loss: each session loses < Resolution.
	roundTol := Resolution*float64(nSessions) + tiny

	total := 0.0
	globalMax := 0.0
	chargerSum := map[string]float64{}
	chargerMax := map[string]float64{}
	for _, ch := range in.Chargers {
		for _, s := range ch.Sessions {
			v, ok := got[s.Key]
			if !ok {
				t.Fatalf("session %v missing from result", s.Key)
			}
			if v < 0 {
				t.Fatalf("negative setpoint %v for %v", v, s.Key)
			}
			if v > nonNeg(s.CapKW)+tiny {
				t.Fatalf("session %v: setpoint %v exceeds cap %v", s.Key, v, s.CapKW)
			}
			total += v
			chargerSum[ch.ID] += v
			chargerMax[ch.ID] = math.Max(chargerMax[ch.ID], v)
			globalMax = math.Max(globalMax, v)
		}
		if chargerSum[ch.ID] > ch.MaxKW+tiny {
			t.Fatalf("charger %s: sum %v exceeds max %v", ch.ID, chargerSum[ch.ID], ch.MaxKW)
		}
	}
	if total > nonNeg(in.GridKW)+tiny {
		t.Fatalf("total %v exceeds grid %v", total, in.GridKW)
	}

	gridSaturated := total >= nonNeg(in.GridKW)-roundTol
	for _, ch := range in.Chargers {
		chSaturated := chargerSum[ch.ID] >= ch.MaxKW-roundTol
		for _, s := range ch.Sessions {
			v := got[s.Key]
			if v >= nonNeg(s.CapKW)-Resolution-tiny {
				continue // session is (within rounding) fully served
			}
			// Under-served session: it must be blocked by a saturated constraint...
			if !chSaturated && !gridSaturated {
				t.Fatalf("session %v under-served (%v < cap %v) but neither charger %s (%v/%v) nor grid (%v/%v) is saturated",
					s.Key, v, s.CapKW, ch.ID, chargerSum[ch.ID], ch.MaxKW, total, in.GridKW)
			}
			// ...and get at least as much as anyone sharing that bottleneck (max-min).
			if chSaturated {
				if v < chargerMax[ch.ID]-Resolution-tiny {
					t.Fatalf("session %v (%v) gets less than a peer on saturated charger %s (%v)",
						s.Key, v, ch.ID, chargerMax[ch.ID])
				}
			} else if v < globalMax-Resolution-tiny {
				t.Fatalf("grid-bound session %v (%v) gets less than another session (%v)", s.Key, v, globalMax)
			}
		}
	}
}

func BenchmarkAllocate_FullBriefStation(b *testing.B) {
	in := briefStation(400, map[string]float64{"CP1/1": 300, "CP1/2": 120, "CP2/1": 300, "CP2/2": 60})
	for b.Loop() {
		Allocate(in)
	}
}
