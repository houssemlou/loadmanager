// Package allocator computes power setpoints for a charging station.
//
// It is a pure, deterministic function with no I/O and no shared state:
// given the effective grid budget and the per-charger / per-session caps,
// it returns how many kW each session may draw. This makes the core of
// the system trivially testable and independent from HTTP, locking, etc.
//
// # Policy: hierarchical max-min fairness ("water-filling")
//
// Constraints form a tree: grid → charger → session (connector + vehicle
// limits are pre-combined into the session cap by the caller).
//
// The result is the unique max-min fair allocation: every session gets an
// equal share, except sessions that are capped lower (by their vehicle /
// connector or by a saturated charger); the power they cannot use is
// redistributed among the others.
//
// # Algorithm: progressive filling
//
// All unfrozen sessions are raised together by the largest step δ that
// does not violate any constraint. After each step, at least one
// constraint becomes tight, and the sessions it bounds are frozen:
//
//   - a session that reached its own cap,
//   - all sessions of a charger that reached its max power,
//   - everyone, if the grid budget is exhausted.
//
// Each iteration freezes at least one session or charger, so the loop runs
// at most (#sessions + #chargers + 1) times: O(n·(n+k)), i.e. microseconds
// for any real station. No binary search, no tuning parameters.
package allocator

import "math"

// SessionKey identifies a session by its physical location.
type SessionKey struct {
	ChargerID   string
	ConnectorID string
}

// Session is one active charging session on a charger.
type Session struct {
	Key SessionKey
	// CapKW is the most this session can use: min(vehicle demand, connector max).
	CapKW float64
}

// Charger groups sessions sharing the same power electronics.
type Charger struct {
	ID       string
	MaxKW    float64
	Sessions []Session
}

// Input is everything the allocator needs.
type Input struct {
	// GridKW is the distributable budget (grid capacity minus safety margin).
	GridKW   float64
	Chargers []Charger
}

// Result maps every input session to its setpoint in kW.
type Result map[SessionKey]float64

// Resolution is the setpoint granularity (kW). Setpoints are rounded DOWN
// to this step so the rounded sum can never exceed the budget.
const Resolution = 0.1

// eps absorbs floating-point residue when comparing remaining capacity to 0.
const eps = 1e-9

type sessionState struct {
	key     SessionKey
	cap     float64
	alloc   float64
	charger int
	frozen  bool
}

// Allocate returns the max-min fair allocation for the given input.
// Every session in the input appears in the result (possibly with 0).
func Allocate(in Input) Result {
	res := make(Result)

	gridRem := nonNeg(in.GridKW)
	chRem := make([]float64, len(in.Chargers))
	var sessions []*sessionState

	for ci, ch := range in.Chargers {
		chRem[ci] = nonNeg(ch.MaxKW)
		for _, s := range ch.Sessions {
			res[s.Key] = 0
			c := nonNeg(s.CapKW)
			if c <= eps {
				continue // nothing to give: never enters the filling loop
			}
			sessions = append(sessions, &sessionState{key: s.Key, cap: c, charger: ci})
		}
	}

	// Upper bound on iterations: a defensive guard, never hit in practice.
	maxIter := len(sessions) + len(in.Chargers) + 2
	for range maxIter {
		active, perCharger := countActive(sessions, len(in.Chargers))
		if active == 0 {
			break
		}

		// Largest equal step every active session can take.
		delta := gridRem / float64(active)
		for ci, n := range perCharger {
			if n > 0 {
				delta = math.Min(delta, chRem[ci]/float64(n))
			}
		}
		for _, s := range sessions {
			if !s.frozen {
				delta = math.Min(delta, s.cap-s.alloc)
			}
		}
		delta = nonNeg(delta)

		for _, s := range sessions {
			if s.frozen {
				continue
			}
			s.alloc += delta
			chRem[s.charger] -= delta
			gridRem -= delta
		}

		// Freeze whatever became tight.
		if gridRem <= eps {
			break // grid exhausted: everybody stays where they are
		}
		for _, s := range sessions {
			if s.frozen {
				continue
			}
			if s.cap-s.alloc <= eps || chRem[s.charger] <= eps {
				s.frozen = true
			}
		}
	}

	for _, s := range sessions {
		res[s.key] = math.Floor(s.alloc*10) / 10 // round down to Resolution
	}
	return res
}

func countActive(sessions []*sessionState, nChargers int) (int, []int) {
	perCharger := make([]int, nChargers)
	active := 0
	for _, s := range sessions {
		if !s.frozen {
			active++
			perCharger[s.charger]++
		}
	}
	return active, perCharger
}

func nonNeg(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	return v
}
