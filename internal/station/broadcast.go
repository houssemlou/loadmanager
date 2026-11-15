package station

import (
	"sort"
	"sync"
)

// SetpointEvent is pushed to subscribers whenever a connector's setpoint
// changes. AllocationSeq lets a charger discard out-of-order updates.
type SetpointEvent struct {
	AllocationSeq uint64  `json:"allocation_seq"`
	ChargerID     string  `json:"charger_id"`
	ConnectorID   string  `json:"connector_id"`
	SetpointKW    float64 `json:"setpoint_kw"`
}

type connectorKey struct{ chargerID, connectorID string }

func keyOf(ev SetpointEvent) connectorKey { return connectorKey{ev.ChargerID, ev.ConnectorID} }

// Subscription keeps only the latest pending setpoint per connector, so a slow
// reader never falls behind. Wait on Notify(), then call Drain().
type Subscription struct {
	chargerID string        // "" = all chargers
	notify    chan struct{} // capacity 1: "something is pending"

	mu        sync.Mutex
	pending   map[connectorKey]SetpointEvent
	order     []connectorKey
	delivered map[connectorKey]float64 // last value handed to this reader
	closed    bool
}

func newSubscription(chargerID string, snapshot []SetpointEvent) *Subscription {
	s := &Subscription{
		chargerID: chargerID,
		notify:    make(chan struct{}, 1),
		pending:   make(map[connectorKey]SetpointEvent),
		delivered: make(map[connectorKey]float64),
	}
	for _, ev := range snapshot {
		s.delivered[keyOf(ev)] = ev.SetpointKW
	}
	return s
}

// Notify signals that Drain has something new. It is closed when the subscription ends.
func (s *Subscription) Notify() <-chan struct{} { return s.notify }

// Drain returns changed setpoints, decreases first (vs. what this reader last got).
func (s *Subscription) Drain() []SetpointEvent {
	s.mu.Lock()
	defer s.mu.Unlock()

	evs := make([]SetpointEvent, 0, len(s.order))
	for _, k := range s.order {
		if ev := s.pending[k]; ev.SetpointKW != s.delivered[k] {
			evs = append(evs, ev)
		}
	}
	isDecrease := func(ev SetpointEvent) bool { return ev.SetpointKW < s.delivered[keyOf(ev)] }
	sort.SliceStable(evs, func(i, j int) bool { return isDecrease(evs[i]) && !isDecrease(evs[j]) })

	for _, ev := range evs {
		s.delivered[keyOf(ev)] = ev.SetpointKW
	}
	clear(s.pending)
	s.order = s.order[:0]
	return evs
}

// offer replaces the pending value for the event's connector. It never blocks.
func (s *Subscription) offer(ev SetpointEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	k := keyOf(ev)
	if _, ok := s.pending[k]; !ok {
		s.order = append(s.order, k)
	}
	s.pending[k] = ev
	select {
	case s.notify <- struct{}{}:
	default: // a wake-up is already pending
	}
}

func (s *Subscription) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.notify)
	}
}

// hub fans events out to subscribers. It has no lock of its own: it is only
// accessed while holding Station.mu, which also keeps events in seq order.
type hub struct {
	subs map[*Subscription]struct{}
}

func newHub() *hub { return &hub{subs: make(map[*Subscription]struct{})} }

func (h *hub) add(chargerID string, snapshot []SetpointEvent) *Subscription {
	sub := newSubscription(chargerID, snapshot)
	h.subs[sub] = struct{}{}
	return sub
}

func (h *hub) remove(sub *Subscription) {
	if _, ok := h.subs[sub]; ok {
		delete(h.subs, sub)
		sub.close()
	}
}

// publish never blocks: each subscriber just overwrites its latest value.
func (h *hub) publish(ev SetpointEvent) {
	for sub := range h.subs {
		if sub.chargerID == "" || sub.chargerID == ev.ChargerID {
			sub.offer(ev)
		}
	}
}
