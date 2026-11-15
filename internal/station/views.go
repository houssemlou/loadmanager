package station

import (
	"fmt"
	"math"
	"time"
)

// StationView is the OPS-facing snapshot of the whole station.
type StationView struct {
	StationID           string  `json:"station_id"`
	GridCapacityKW      float64 `json:"grid_capacity_kw"`
	SafetyMarginKW      float64 `json:"safety_margin_kw"`
	EffectiveCapacityKW float64 `json:"effective_capacity_kw"`
	AllocatedKW         float64 `json:"allocated_kw"`
	HeadroomKW          float64 `json:"headroom_kw"`
	// UnservedDemandKW is what vehicles could take but don't get: > 0 means
	// the station is power-constrained.
	UnservedDemandKW float64       `json:"unserved_demand_kw"`
	ActiveSessions   int           `json:"active_sessions"`
	AllocationSeq    uint64        `json:"allocation_seq"`
	Chargers         []ChargerView `json:"chargers"`
}

// ChargerView is one charger's live state.
type ChargerView struct {
	ID          string          `json:"id"`
	Status      ChargerStatus   `json:"status"`
	MaxPowerKW  float64         `json:"max_power_kw"`
	AllocatedKW float64         `json:"allocated_kw"`
	Connectors  []ConnectorView `json:"connectors"`
}

// ConnectorView is one connector's live state; also the polling endpoint body.
type ConnectorView struct {
	ChargerID     string          `json:"charger_id"`
	ID            string          `json:"id"`
	Type          string          `json:"type,omitempty"`
	MaxPowerKW    float64         `json:"max_power_kw"`
	Status        ConnectorStatus `json:"status"`
	SetpointKW    float64         `json:"setpoint_kw"`
	AllocationSeq uint64          `json:"allocation_seq"`
	Session       *SessionView    `json:"session,omitempty"`
}

// SessionView is an active session as seen by OPS.
type SessionView struct {
	VehicleMaxPowerKW float64      `json:"vehicle_max_power_kw"`
	ChargingCurve     []CurvePoint `json:"charging_curve,omitempty"`
	SoCPct            *float64     `json:"soc_pct,omitempty"`
	RequestedPowerKW  *float64     `json:"requested_power_kw,omitempty"`
	DemandKW          float64      `json:"demand_kw"`
	StartedAt         time.Time    `json:"started_at"`
	UpdatedAt         time.Time    `json:"updated_at"`
}

// Status returns the full station snapshot.
func (s *Station) Status() StationView {
	s.mu.Lock()
	defer s.mu.Unlock()

	v := StationView{
		StationID:           s.cfg.StationID,
		GridCapacityKW:      s.cfg.GridCapacityKW,
		SafetyMarginKW:      s.cfg.SafetyMarginKW,
		EffectiveCapacityKW: s.cfg.EffectiveGridKW(),
		AllocationSeq:       s.seq,
	}
	unserved := 0.0
	for _, ch := range s.chargers {
		cv := s.chargerView(ch)
		v.AllocatedKW += cv.AllocatedKW
		for _, cn := range ch.connectors {
			if cn.session == nil {
				continue
			}
			v.ActiveSessions++
			want := math.Min(cn.session.demand(), cn.cfg.MaxPowerKW)
			unserved += math.Max(want-cn.setpointKW, 0)
		}
		v.Chargers = append(v.Chargers, cv)
	}
	v.AllocatedKW = roundKW(v.AllocatedKW)
	v.HeadroomKW = roundKW(v.EffectiveCapacityKW - v.AllocatedKW)
	v.UnservedDemandKW = roundKW(unserved)
	return v
}

// Charger returns one charger's snapshot.
func (s *Station) Charger(chargerID string) (ChargerView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.byID[chargerID]
	if !ok {
		return ChargerView{}, fmt.Errorf("%w: %s", ErrUnknownCharger, chargerID)
	}
	return s.chargerView(ch), nil
}

// Connector returns one connector's snapshot (the polling fallback for setpoints).
func (s *Station) Connector(chargerID, connectorID string) (ConnectorView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, cn, err := s.lookup(chargerID, connectorID)
	if err != nil {
		return ConnectorView{}, err
	}
	return s.connectorView(ch, cn), nil
}

// Caller must hold s.mu.
func (s *Station) chargerView(ch *chargerState) ChargerView {
	cv := ChargerView{ID: ch.cfg.ID, Status: ch.status, MaxPowerKW: ch.cfg.MaxPowerKW}
	for _, cn := range ch.connectors {
		cv.AllocatedKW += cn.setpointKW
		cv.Connectors = append(cv.Connectors, s.connectorView(ch, cn))
	}
	cv.AllocatedKW = roundKW(cv.AllocatedKW)
	return cv
}

// Caller must hold s.mu.
func (s *Station) connectorView(ch *chargerState, cn *connectorState) ConnectorView {
	v := ConnectorView{
		ChargerID:     ch.cfg.ID,
		ID:            cn.cfg.ID,
		Type:          cn.cfg.Type,
		MaxPowerKW:    cn.cfg.MaxPowerKW,
		SetpointKW:    cn.setpointKW,
		AllocationSeq: s.seq,
	}
	switch {
	case ch.status != ChargerAvailable:
		v.Status = ConnectorUnavailable
	case cn.session != nil:
		v.Status = ConnectorOccupied
	default:
		v.Status = ConnectorAvailable
	}
	if ss := cn.session; ss != nil {
		v.Session = &SessionView{
			VehicleMaxPowerKW: ss.vehicleMaxKW,
			ChargingCurve:     ss.curve,
			SoCPct:            copyPtr(ss.socPct),
			RequestedPowerKW:  copyPtr(ss.requestedKW),
			DemandKW:          roundKW(ss.demand()),
			StartedAt:         ss.startedAt,
			UpdatedAt:         ss.updatedAt,
		}
	}
	return v
}
