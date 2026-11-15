// Package station holds the live state of a charging station (sessions,
// charger availability, current setpoints) and re-runs the allocator on
// every event.
//
// Concurrency model: a single mutex guards all state. Every mutation runs
// validate → mutate → reallocate → publish while holding it, so events are
// totally ordered, setpoints are always consistent with the state, and
// subscribers see changes in allocation order. Allocation takes
// microseconds, so the lock is never a latency concern at station scale.
package station

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/houssemlou/loadmanager/internal/allocator"
	"github.com/houssemlou/loadmanager/internal/config"
)

// Domain errors, mapped to HTTP status codes by the API layer.
var (
	ErrInvalidInput       = errors.New("invalid input")
	ErrUnknownCharger     = errors.New("unknown charger")
	ErrUnknownConnector   = errors.New("unknown connector")
	ErrConnectorOccupied  = errors.New("connector already has an active session")
	ErrNoSession          = errors.New("no active session on connector")
	ErrChargerUnavailable = errors.New("charger is not available")
)

// ChargerStatus is the operational state of a charger.
type ChargerStatus string

const (
	ChargerAvailable ChargerStatus = "available"
	ChargerFaulted   ChargerStatus = "faulted"
	ChargerOffline   ChargerStatus = "offline"
)

// Valid reports whether s is a known status.
func (s ChargerStatus) Valid() bool {
	switch s {
	case ChargerAvailable, ChargerFaulted, ChargerOffline:
		return true
	}
	return false
}

// ConnectorStatus is the OPS-facing availability of a connector.
type ConnectorStatus string

const (
	ConnectorAvailable   ConnectorStatus = "available"   // free, charger healthy
	ConnectorOccupied    ConnectorStatus = "occupied"    // vehicle plugged in
	ConnectorUnavailable ConnectorStatus = "unavailable" // charger faulted/offline
)

// SessionStart is the payload of a vehicle-connect event.
type SessionStart struct {
	VehicleMaxPowerKW float64
	ChargingCurve     []CurvePoint
	SoCPct            *float64
	RequestedPowerKW  *float64
}

// SessionUpdate is the payload of an in-session update (nil = unchanged).
type SessionUpdate struct {
	SoCPct           *float64
	RequestedPowerKW *float64
}

type session struct {
	vehicleMaxKW float64
	curve        []CurvePoint
	socPct       *float64
	requestedKW  *float64
	startedAt    time.Time
	updatedAt    time.Time
}

func (s *session) demand() float64 {
	return Demand(s.vehicleMaxKW, s.curve, s.socPct, s.requestedKW)
}

type connectorState struct {
	cfg        config.Connector
	session    *session
	setpointKW float64
}

type chargerState struct {
	cfg        config.Charger
	status     ChargerStatus
	connectors []*connectorState // config order, for deterministic output
	byID       map[string]*connectorState
}

// Station is the live, in-memory state of one charging station.
type Station struct {
	mu       sync.Mutex
	cfg      *config.Config
	chargers []*chargerState
	byID     map[string]*chargerState
	seq      uint64
	hub      *hub
	log      *slog.Logger
	now      func() time.Time
}

// New builds a station from a validated config. All chargers start available.
func New(cfg *config.Config, log *slog.Logger) *Station {
	if log == nil {
		log = slog.Default()
	}
	s := &Station{
		cfg:  cfg,
		byID: make(map[string]*chargerState),
		hub:  newHub(),
		log:  log,
		now:  time.Now,
	}
	for _, chCfg := range cfg.Chargers {
		ch := &chargerState{cfg: chCfg, status: ChargerAvailable, byID: make(map[string]*connectorState)}
		for _, cnCfg := range chCfg.Connectors {
			cn := &connectorState{cfg: cnCfg}
			ch.connectors = append(ch.connectors, cn)
			ch.byID[cnCfg.ID] = cn
		}
		s.chargers = append(s.chargers, ch)
		s.byID[chCfg.ID] = ch
	}
	return s
}

// Config returns the static station configuration.
func (s *Station) Config() *config.Config { return s.cfg }

// SessionResult is returned by session events: the caller's new setpoint.
type SessionResult struct {
	ChargerID     string  `json:"charger_id"`
	ConnectorID   string  `json:"connector_id"`
	DemandKW      float64 `json:"demand_kw"`
	SetpointKW    float64 `json:"setpoint_kw"`
	AllocationSeq uint64  `json:"allocation_seq"`
}

// StartSession registers a vehicle on a connector and rebalances.
func (s *Station) StartSession(chargerID, connectorID string, req SessionStart) (SessionResult, error) {
	if err := validateStart(req); err != nil {
		return SessionResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ch, cn, err := s.lookup(chargerID, connectorID)
	if err != nil {
		return SessionResult{}, err
	}
	if ch.status != ChargerAvailable {
		return SessionResult{}, fmt.Errorf("%w: %s is %s", ErrChargerUnavailable, chargerID, ch.status)
	}
	if cn.session != nil {
		return SessionResult{}, fmt.Errorf("%w: %s/%s", ErrConnectorOccupied, chargerID, connectorID)
	}
	now := s.now()
	cn.session = &session{
		vehicleMaxKW: req.VehicleMaxPowerKW,
		curve:        append([]CurvePoint(nil), req.ChargingCurve...),
		socPct:       copyPtr(req.SoCPct),
		requestedKW:  copyPtr(req.RequestedPowerKW),
		startedAt:    now,
		updatedAt:    now,
	}
	s.rebalance("session_start", chargerID, connectorID)
	return s.result(ch, cn), nil
}

// UpdateSession applies SoC / requested-power changes and rebalances.
func (s *Station) UpdateSession(chargerID, connectorID string, upd SessionUpdate) (SessionResult, error) {
	if err := validateUpdate(upd); err != nil {
		return SessionResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ch, cn, err := s.lookup(chargerID, connectorID)
	if err != nil {
		return SessionResult{}, err
	}
	if cn.session == nil {
		return SessionResult{}, fmt.Errorf("%w: %s/%s", ErrNoSession, chargerID, connectorID)
	}
	if upd.SoCPct != nil {
		cn.session.socPct = copyPtr(upd.SoCPct)
	}
	if upd.RequestedPowerKW != nil {
		cn.session.requestedKW = copyPtr(upd.RequestedPowerKW)
	}
	cn.session.updatedAt = s.now()
	s.rebalance("session_update", chargerID, connectorID)
	return s.result(ch, cn), nil
}

// StopSession removes the vehicle from a connector and rebalances.
func (s *Station) StopSession(chargerID, connectorID string) (SessionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ch, cn, err := s.lookup(chargerID, connectorID)
	if err != nil {
		return SessionResult{}, err
	}
	if cn.session == nil {
		return SessionResult{}, fmt.Errorf("%w: %s/%s", ErrNoSession, chargerID, connectorID)
	}
	cn.session = nil
	s.rebalance("session_stop", chargerID, connectorID)
	return s.result(ch, cn), nil
}

// SetChargerStatus marks a charger available/faulted/offline and rebalances.
// Sessions on an unavailable charger are kept but receive 0 kW.
func (s *Station) SetChargerStatus(chargerID string, status ChargerStatus) (ChargerView, error) {
	if !status.Valid() {
		return ChargerView{}, fmt.Errorf("%w: status must be one of available, faulted, offline", ErrInvalidInput)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ch, ok := s.byID[chargerID]
	if !ok {
		return ChargerView{}, fmt.Errorf("%w: %s", ErrUnknownCharger, chargerID)
	}
	if ch.status != status {
		ch.status = status
		s.rebalance("charger_status", chargerID, "")
	}
	return s.chargerView(ch), nil
}

// Subscribe registers a setpoint listener (chargerID "" = all chargers) and
// returns a snapshot of current setpoints. Registration and snapshot happen
// atomically, so no event can be missed between the two.
func (s *Station) Subscribe(chargerID string) (*Subscription, []SetpointEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if chargerID != "" {
		if _, ok := s.byID[chargerID]; !ok {
			return nil, nil, fmt.Errorf("%w: %s", ErrUnknownCharger, chargerID)
		}
	}
	var snapshot []SetpointEvent
	for _, ch := range s.chargers {
		if chargerID != "" && ch.cfg.ID != chargerID {
			continue
		}
		for _, cn := range ch.connectors {
			snapshot = append(snapshot, SetpointEvent{
				AllocationSeq: s.seq, ChargerID: ch.cfg.ID, ConnectorID: cn.cfg.ID, SetpointKW: cn.setpointKW,
			})
		}
	}
	return s.hub.add(chargerID, snapshot), snapshot, nil
}

// Unsubscribe ends a subscription. Safe to call more than once.
func (s *Station) Unsubscribe(sub *Subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hub.remove(sub)
}

// rebalance recomputes all setpoints and publishes the changes.
// Caller must hold s.mu.
func (s *Station) rebalance(reason, chargerID, connectorID string) {
	started := time.Now()

	in := allocator.Input{GridKW: s.cfg.EffectiveGridKW()}
	for _, ch := range s.chargers {
		if ch.status != ChargerAvailable {
			continue // its sessions are absent from the result → 0 kW
		}
		ac := allocator.Charger{ID: ch.cfg.ID, MaxKW: ch.cfg.MaxPowerKW}
		for _, cn := range ch.connectors {
			if cn.session == nil {
				continue
			}
			ac.Sessions = append(ac.Sessions, allocator.Session{
				Key:   allocator.SessionKey{ChargerID: ch.cfg.ID, ConnectorID: cn.cfg.ID},
				CapKW: math.Min(cn.session.demand(), cn.cfg.MaxPowerKW),
			})
		}
		in.Chargers = append(in.Chargers, ac)
	}
	res := allocator.Allocate(in)

	type change struct {
		ch       *chargerState
		cn       *connectorState
		old, new float64
	}
	var changes []change
	for _, ch := range s.chargers {
		for _, cn := range ch.connectors {
			newKW := res[allocator.SessionKey{ChargerID: ch.cfg.ID, ConnectorID: cn.cfg.ID}] // missing → 0
			if newKW != cn.setpointKW {
				changes = append(changes, change{ch, cn, cn.setpointKW, newKW})
			}
		}
	}
	// Publish decreases before increases: if chargers apply setpoints in
	// arrival order, the station total never transiently exceeds the grid.
	sort.SliceStable(changes, func(i, j int) bool {
		return changes[i].new-changes[i].old < 0 && changes[j].new-changes[j].old >= 0
	})

	s.seq++
	for _, c := range changes {
		c.cn.setpointKW = c.new
		s.hub.publish(SetpointEvent{
			AllocationSeq: s.seq, ChargerID: c.ch.cfg.ID, ConnectorID: c.cn.cfg.ID, SetpointKW: c.new,
		})
	}

	s.log.Info("rebalanced",
		"reason", reason, "charger", chargerID, "connector", connectorID,
		"allocation_seq", s.seq, "changes", len(changes),
		"allocated_kw", s.allocatedKW(), "duration_us", time.Since(started).Microseconds())
}

func (s *Station) lookup(chargerID, connectorID string) (*chargerState, *connectorState, error) {
	ch, ok := s.byID[chargerID]
	if !ok {
		return nil, nil, fmt.Errorf("%w: %s", ErrUnknownCharger, chargerID)
	}
	cn, ok := ch.byID[connectorID]
	if !ok {
		return nil, nil, fmt.Errorf("%w: %s/%s", ErrUnknownConnector, chargerID, connectorID)
	}
	return ch, cn, nil
}

func (s *Station) result(ch *chargerState, cn *connectorState) SessionResult {
	r := SessionResult{
		ChargerID: ch.cfg.ID, ConnectorID: cn.cfg.ID, SetpointKW: cn.setpointKW, AllocationSeq: s.seq,
	}
	if cn.session != nil {
		r.DemandKW = roundKW(cn.session.demand())
	}
	return r
}

func (s *Station) allocatedKW() float64 {
	total := 0.0
	for _, ch := range s.chargers {
		for _, cn := range ch.connectors {
			total += cn.setpointKW
		}
	}
	return roundKW(total)
}

func validateStart(req SessionStart) error {
	if !finite(req.VehicleMaxPowerKW) || req.VehicleMaxPowerKW <= 0 {
		return fmt.Errorf("%w: vehicle_max_power_kw must be > 0", ErrInvalidInput)
	}
	if req.ChargingCurve != nil {
		if err := ValidateCurve(req.ChargingCurve); err != nil {
			return err
		}
	}
	return validateUpdate(SessionUpdate{SoCPct: req.SoCPct, RequestedPowerKW: req.RequestedPowerKW})
}

func validateUpdate(upd SessionUpdate) error {
	if upd.SoCPct != nil && !validSoC(*upd.SoCPct) {
		return fmt.Errorf("%w: soc_pct must be within [0,100]", ErrInvalidInput)
	}
	if upd.RequestedPowerKW != nil && !finiteNonNeg(*upd.RequestedPowerKW) {
		return fmt.Errorf("%w: requested_power_kw must be >= 0", ErrInvalidInput)
	}
	return nil
}

func copyPtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// roundKW strips float noise for display (e.g. 266.60000000000002 → 266.6).
func roundKW(v float64) float64 { return math.Round(v*1000) / 1000 }
