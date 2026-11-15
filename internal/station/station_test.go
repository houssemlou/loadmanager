package station

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/houssemlou/loadmanager/internal/config"
)

// briefConfig is the station from the assignment: 400 kW grid,
// 2 × 300 kW chargers with 2 CCS connectors each.
func briefConfig() *config.Config {
	cfg, err := config.Parse([]byte(`{
	  "station_id": "test", "grid_capacity_kw": 400,
	  "chargers": [
	    {"id": "CP1", "max_power_kw": 300, "connectors": [{"id": "1", "type": "CCS"}, {"id": "2", "type": "CCS"}]},
	    {"id": "CP2", "max_power_kw": 300, "connectors": [{"id": "1", "type": "CCS"}, {"id": "2", "type": "CCS"}]}
	  ]}`))
	if err != nil {
		panic(err)
	}
	return cfg
}

func newTestStation() *Station {
	return New(briefConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func mustStart(t *testing.T, s *Station, ch, cn string, maxKW float64) SessionResult {
	t.Helper()
	r, err := s.StartSession(ch, cn, SessionStart{VehicleMaxPowerKW: maxKW})
	if err != nil {
		t.Fatalf("start %s/%s: %v", ch, cn, err)
	}
	return r
}

func setpoint(t *testing.T, s *Station, ch, cn string) float64 {
	t.Helper()
	v, err := s.Connector(ch, cn)
	if err != nil {
		t.Fatal(err)
	}
	return v.SetpointKW
}

func TestStation_BriefFlow(t *testing.T) {
	s := newTestStation()

	if r := mustStart(t, s, "CP1", "1", 350); r.SetpointKW != 300 {
		t.Fatalf("lone car: got %v, want 300", r.SetpointKW)
	}
	if r := mustStart(t, s, "CP2", "1", 350); r.SetpointKW != 200 {
		t.Fatalf("second car: got %v, want 200", r.SetpointKW)
	}
	if got := setpoint(t, s, "CP1", "1"); got != 200 {
		t.Fatalf("first car after second arrives: got %v, want 200", got)
	}
	if _, err := s.StopSession("CP1", "1"); err != nil {
		t.Fatal(err)
	}
	if got := setpoint(t, s, "CP2", "1"); got != 300 {
		t.Fatalf("second car after first leaves: got %v, want 300", got)
	}
	if got := setpoint(t, s, "CP1", "1"); got != 0 {
		t.Fatalf("disconnected connector should be 0, got %v", got)
	}
}

func TestStation_CurveTaperRedistributes(t *testing.T) {
	s := newTestStation()
	mustStart(t, s, "CP1", "1", 300)
	_, err := s.StartSession("CP2", "1", SessionStart{
		VehicleMaxPowerKW: 300, ChargingCurve: taper, SoCPct: ptr(20),
	})
	if err != nil {
		t.Fatal(err)
	}
	// SoC 20% → curve says 150 → CP2/1 takes 150, CP1/1 gets the rest (250).
	if a, b := setpoint(t, s, "CP1", "1"), setpoint(t, s, "CP2", "1"); a != 250 || b != 150 {
		t.Fatalf("got CP1/1=%v CP2/1=%v, want 250/150", a, b)
	}
	// Car tapers at 90% → 30 kW; CP1/1 is capped by its charger at 300.
	if _, err := s.UpdateSession("CP2", "1", SessionUpdate{SoCPct: ptr(90)}); err != nil {
		t.Fatal(err)
	}
	if a, b := setpoint(t, s, "CP1", "1"), setpoint(t, s, "CP2", "1"); a != 300 || b != 30 {
		t.Fatalf("after taper got CP1/1=%v CP2/1=%v, want 300/30", a, b)
	}
}

func TestStation_ChargerFaultRedistributesAndRecovers(t *testing.T) {
	s := newTestStation()
	mustStart(t, s, "CP1", "1", 300)
	mustStart(t, s, "CP2", "1", 300)

	cv, err := s.SetChargerStatus("CP1", ChargerFaulted)
	if err != nil {
		t.Fatal(err)
	}
	if cv.Connectors[0].Status != ConnectorUnavailable || cv.Connectors[0].SetpointKW != 0 {
		t.Fatalf("faulted charger connector: %+v", cv.Connectors[0])
	}
	if got := setpoint(t, s, "CP2", "1"); got != 300 {
		t.Fatalf("healthy charger should get freed power: got %v, want 300", got)
	}
	if _, err := s.StartSession("CP1", "2", SessionStart{VehicleMaxPowerKW: 50}); !errors.Is(err, ErrChargerUnavailable) {
		t.Fatalf("new session on faulted charger: want ErrChargerUnavailable, got %v", err)
	}

	if _, err := s.SetChargerStatus("CP1", ChargerAvailable); err != nil {
		t.Fatal(err)
	}
	if a, b := setpoint(t, s, "CP1", "1"), setpoint(t, s, "CP2", "1"); a != 200 || b != 200 {
		t.Fatalf("after recovery got %v/%v, want 200/200", a, b)
	}
}

func TestStation_Errors(t *testing.T) {
	s := newTestStation()
	mustStart(t, s, "CP1", "1", 100)

	cases := []struct {
		name string
		err  error
		want error
	}{
		{"unknown charger", must2(s.StartSession("NOPE", "1", SessionStart{VehicleMaxPowerKW: 1})), ErrUnknownCharger},
		{"unknown connector", must2(s.StartSession("CP1", "9", SessionStart{VehicleMaxPowerKW: 1})), ErrUnknownConnector},
		{"occupied", must2(s.StartSession("CP1", "1", SessionStart{VehicleMaxPowerKW: 1})), ErrConnectorOccupied},
		{"zero vehicle max", must2(s.StartSession("CP1", "2", SessionStart{VehicleMaxPowerKW: 0})), ErrInvalidInput},
		{"bad soc", must2(s.StartSession("CP1", "2", SessionStart{VehicleMaxPowerKW: 1, SoCPct: ptr(101)})), ErrInvalidInput},
		{"bad curve", must2(s.StartSession("CP1", "2", SessionStart{VehicleMaxPowerKW: 1, ChargingCurve: []CurvePoint{}})), ErrInvalidInput},
		{"update without session", must2(s.UpdateSession("CP1", "2", SessionUpdate{SoCPct: ptr(1)})), ErrNoSession},
		{"negative request", must2(s.UpdateSession("CP1", "1", SessionUpdate{RequestedPowerKW: ptr(-1)})), ErrInvalidInput},
		{"stop without session", must2(s.StopSession("CP2", "1")), ErrNoSession},
		{"bad charger status", must2(s.SetChargerStatus("CP1", "broken")), ErrInvalidInput},
	}
	for _, tc := range cases {
		if !errors.Is(tc.err, tc.want) {
			t.Errorf("%s: want %v, got %v", tc.name, tc.want, tc.err)
		}
	}
}

func must2[T any](_ T, err error) error { return err }

// drain waits for the subscription's next wake-up and returns what is pending.
func drain(t *testing.T, sub *Subscription) []SetpointEvent {
	t.Helper()
	select {
	case _, ok := <-sub.Notify():
		if !ok {
			t.Fatal("subscription closed")
		}
	case <-time.After(time.Second):
		t.Fatal("no notification within 1s")
	}
	return sub.Drain()
}

func TestStation_PublishesDecreasesBeforeIncreases(t *testing.T) {
	s := newTestStation()
	mustStart(t, s, "CP1", "1", 300)
	mustStart(t, s, "CP2", "1", 300) // 200/200

	sub, _, err := s.Subscribe("")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Unsubscribe(sub)

	// CP2 car caps itself at 50 → CP2/1 decreases to 50, CP1/1 increases to 300.
	if _, err := s.UpdateSession("CP2", "1", SessionUpdate{RequestedPowerKW: ptr(50)}); err != nil {
		t.Fatal(err)
	}
	evs := drain(t, sub)
	if len(evs) != 2 {
		t.Fatalf("want 2 events, got %+v", evs)
	}
	if evs[0].ChargerID != "CP2" || evs[0].SetpointKW != 50 {
		t.Fatalf("first event should be the decrease on CP2, got %+v", evs[0])
	}
	if evs[1].ChargerID != "CP1" || evs[1].SetpointKW != 300 {
		t.Fatalf("second event should be the increase on CP1, got %+v", evs[1])
	}
	if evs[0].AllocationSeq != evs[1].AllocationSeq {
		t.Fatalf("events of one rebalance must share a seq: %d vs %d", evs[0].AllocationSeq, evs[1].AllocationSeq)
	}
}

func TestStation_SubscribeSnapshotAndFilter(t *testing.T) {
	s := newTestStation()
	mustStart(t, s, "CP1", "1", 300)

	sub, snap, err := s.Subscribe("CP2")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Unsubscribe(sub)
	if len(snap) != 2 || snap[0].ChargerID != "CP2" {
		t.Fatalf("filtered snapshot should contain CP2's 2 connectors, got %+v", snap)
	}

	mustStart(t, s, "CP2", "1", 300) // CP1/1 drops to 200 (filtered out), CP2/1 rises to 200
	evs := drain(t, sub)
	if len(evs) != 1 || evs[0].ChargerID != "CP2" || evs[0].SetpointKW != 200 {
		t.Fatalf("got %+v, want only CP2/1 → 200 (filter must hide CP1)", evs)
	}

	if _, _, err := s.Subscribe("NOPE"); !errors.Is(err, ErrUnknownCharger) {
		t.Fatalf("subscribe to unknown charger: want ErrUnknownCharger, got %v", err)
	}
}

// A reader that never reads must neither block the station nor be dropped:
// when it finally reads, it gets only the current value of each connector.
func TestStation_SlowSubscriberGetsLatestValueOnly(t *testing.T) {
	s := newTestStation()
	sub, _, _ := s.Subscribe("")
	defer s.Unsubscribe(sub)

	for i := range 1000 { // far more events than any queue would hold
		if _, err := s.StartSession("CP1", "1", SessionStart{VehicleMaxPowerKW: float64(10 + i%200)}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.StopSession("CP1", "1"); err != nil {
			t.Fatal(err)
		}
	}
	mustStart(t, s, "CP1", "1", 300)
	mustStart(t, s, "CP2", "1", 300) // final state: 200 / 200

	evs := drain(t, sub)
	if len(evs) != 2 {
		t.Fatalf("want one latest event per changed connector, got %d: %+v", len(evs), evs)
	}
	for _, ev := range evs {
		if ev.SetpointKW != 200 {
			t.Fatalf("stale value delivered: %+v", ev)
		}
	}
	st := s.Status()
	if evs[0].AllocationSeq != st.AllocationSeq {
		t.Fatalf("delivered seq %d, want latest %d", evs[0].AllocationSeq, st.AllocationSeq)
	}
}

// Catching up across several rebalances must still lower before it raises,
// judged against what this reader last received.
func TestSubscription_DrainOrdersDecreasesAgainstDelivered(t *testing.T) {
	sub := newSubscription("", []SetpointEvent{
		{ChargerID: "CP1", ConnectorID: "1", SetpointKW: 300},
		{ChargerID: "CP2", ConnectorID: "1", SetpointKW: 0},
	})
	sub.offer(SetpointEvent{AllocationSeq: 5, ChargerID: "CP1", ConnectorID: "1", SetpointKW: 100}) // seq 5: decrease
	sub.offer(SetpointEvent{AllocationSeq: 5, ChargerID: "CP2", ConnectorID: "1", SetpointKW: 300}) // seq 5: increase
	sub.offer(SetpointEvent{AllocationSeq: 6, ChargerID: "CP2", ConnectorID: "1", SetpointKW: 300}) // seq 6: same value
	sub.offer(SetpointEvent{AllocationSeq: 6, ChargerID: "CP1", ConnectorID: "1", SetpointKW: 50})  // seq 6: decrease again

	evs := sub.Drain()
	if len(evs) != 2 || evs[0].ChargerID != "CP1" || evs[0].SetpointKW != 50 || evs[1].ChargerID != "CP2" {
		t.Fatalf("want CP1→50 (decrease) before CP2→300 (increase), got %+v", evs)
	}

	sub.offer(SetpointEvent{AllocationSeq: 7, ChargerID: "CP1", ConnectorID: "1", SetpointKW: 50})
	if evs := sub.Drain(); len(evs) != 0 {
		t.Fatalf("a value equal to the last delivered one must be skipped, got %+v", evs)
	}

	sub.close()
	sub.close()              // idempotent
	for range sub.Notify() { // a buffered wake-up may come first, then the channel must end
	}
	sub.offer(SetpointEvent{ChargerID: "CP1", ConnectorID: "1", SetpointKW: 1}) // must not panic after close
}

func TestStation_AllocationSeqIsMonotonic(t *testing.T) {
	s := newTestStation()
	var last uint64
	for i, step := range []func() (SessionResult, error){
		func() (SessionResult, error) { return s.StartSession("CP1", "1", SessionStart{VehicleMaxPowerKW: 100}) },
		func() (SessionResult, error) { return s.UpdateSession("CP1", "1", SessionUpdate{SoCPct: ptr(50)}) },
		func() (SessionResult, error) { return s.StopSession("CP1", "1") },
	} {
		r, err := step()
		if err != nil {
			t.Fatal(err)
		}
		if r.AllocationSeq <= last {
			t.Fatalf("step %d: seq %d not > %d", i, r.AllocationSeq, last)
		}
		last = r.AllocationSeq
	}
}

func TestStation_StatusTotals(t *testing.T) {
	s := newTestStation()
	mustStart(t, s, "CP1", "1", 300)
	mustStart(t, s, "CP1", "2", 300)
	mustStart(t, s, "CP2", "1", 300)

	st := s.Status()
	if st.ActiveSessions != 3 {
		t.Errorf("active sessions = %d, want 3", st.ActiveSessions)
	}
	// 400/3 = 133.3 each → 399.9 allocated, 0.1 headroom, 500.1 unserved.
	if st.AllocatedKW != 399.9 || st.HeadroomKW != 0.1 || st.UnservedDemandKW != 500.1 {
		t.Errorf("totals: allocated=%v headroom=%v unserved=%v", st.AllocatedKW, st.HeadroomKW, st.UnservedDemandKW)
	}
	if st.Chargers[0].Connectors[1].Status != ConnectorOccupied || st.Chargers[1].Connectors[1].Status != ConnectorAvailable {
		t.Errorf("connector statuses wrong: %+v", st.Chargers)
	}
}

// TestStation_ConcurrentEvents hammers the station from many goroutines;
// run with -race. The invariant must hold after every operation.
func TestStation_ConcurrentEvents(t *testing.T) {
	s := newTestStation()
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Go(func() {
			ch := []string{"CP1", "CP2"}[w%2]
			cn := []string{"1", "2"}[(w/2)%2]
			for i := range 200 {
				switch i % 3 {
				case 0:
					_, _ = s.StartSession(ch, cn, SessionStart{VehicleMaxPowerKW: float64(50 + i)})
				case 1:
					_, _ = s.UpdateSession(ch, cn, SessionUpdate{SoCPct: ptr(float64(i % 100))})
				case 2:
					_, _ = s.StopSession(ch, cn)
				}
				if st := s.Status(); st.AllocatedKW > st.EffectiveCapacityKW {
					panic(fmt.Sprintf("allocated %v > capacity %v", st.AllocatedKW, st.EffectiveCapacityKW))
				}
			}
		})
	}
	wg.Wait()
}
