package station

import (
	"fmt"
	"math"
)

// CurvePoint is one point of a vehicle charging curve: the maximum power
// the vehicle accepts at a given state of charge.
type CurvePoint struct {
	SoCPct     float64 `json:"soc_pct"`
	MaxPowerKW float64 `json:"max_power_kw"`
}

// ValidateCurve requires a non-empty curve with strictly increasing SoC in
// [0,100] and non-negative power.
func ValidateCurve(curve []CurvePoint) error {
	if len(curve) == 0 {
		return fmt.Errorf("%w: charging_curve must not be empty", ErrInvalidInput)
	}
	for i, p := range curve {
		if !validSoC(p.SoCPct) {
			return fmt.Errorf("%w: charging_curve[%d].soc_pct must be within [0,100]", ErrInvalidInput, i)
		}
		if !finiteNonNeg(p.MaxPowerKW) {
			return fmt.Errorf("%w: charging_curve[%d].max_power_kw must be >= 0", ErrInvalidInput, i)
		}
		if i > 0 && p.SoCPct <= curve[i-1].SoCPct {
			return fmt.Errorf("%w: charging_curve must be sorted by strictly increasing soc_pct", ErrInvalidInput)
		}
	}
	return nil
}

// CurveAt linearly interpolates the curve at soc, clamping to the first /
// last point outside the curve's range. The curve must be valid.
func CurveAt(curve []CurvePoint, soc float64) float64 {
	if soc <= curve[0].SoCPct {
		return curve[0].MaxPowerKW
	}
	last := curve[len(curve)-1]
	if soc >= last.SoCPct {
		return last.MaxPowerKW
	}
	for i := 1; i < len(curve); i++ {
		a, b := curve[i-1], curve[i]
		if soc <= b.SoCPct {
			t := (soc - a.SoCPct) / (b.SoCPct - a.SoCPct)
			return a.MaxPowerKW + t*(b.MaxPowerKW-a.MaxPowerKW)
		}
	}
	return last.MaxPowerKW // unreachable for a valid curve
}

// Demand is the power a vehicle can currently accept:
// min(vehicle max, curve(SoC) when both are known, explicit request when sent).
func Demand(vehicleMaxKW float64, curve []CurvePoint, socPct, requestedKW *float64) float64 {
	d := vehicleMaxKW
	if len(curve) > 0 && socPct != nil {
		d = math.Min(d, CurveAt(curve, *socPct))
	}
	if requestedKW != nil {
		d = math.Min(d, *requestedKW)
	}
	return math.Max(d, 0)
}

func finite(v float64) bool       { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func finiteNonNeg(v float64) bool { return finite(v) && v >= 0 }
func validSoC(v float64) bool     { return finite(v) && v >= 0 && v <= 100 }
