package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/houssemlou/loadmanager/internal/config"
	"github.com/houssemlou/loadmanager/internal/station"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

func newTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	cfg, err := config.Load("../../configs/example-station.json") // brief: 400 kW, 2×300 kW, 2 CCS each
	if err != nil {
		t.Fatal(err)
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewRouter(station.New(cfg, quiet), quiet)
}

func do(t *testing.T, h http.Handler, method, path, body string) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

const (
	cp1c1 = "/chargers/CP1/connectors/1"
	cp2c1 = "/chargers/CP2/connectors/1"
)

func TestAPI_BriefFlow(t *testing.T) {
	r := newTestRouter(t)

	code, body := do(t, r, http.MethodPost, cp1c1+"/session", `{"vehicle_max_power_kw": 350}`)
	if code != http.StatusCreated || body["setpoint_kw"] != 300.0 {
		t.Fatalf("lone car: %d %v", code, body)
	}

	code, body = do(t, r, http.MethodPost, cp2c1+"/session", `{"vehicle_max_power_kw": 350}`)
	if code != http.StatusCreated || body["setpoint_kw"] != 200.0 {
		t.Fatalf("second car: %d %v", code, body)
	}

	if _, body = do(t, r, http.MethodGet, cp1c1, ""); body["setpoint_kw"] != 200.0 || body["status"] != "occupied" {
		t.Fatalf("first car polled after second arrives: %v", body)
	}

	if code, _ = do(t, r, http.MethodDelete, cp1c1+"/session", ""); code != http.StatusOK {
		t.Fatalf("disconnect: %d", code)
	}
	if _, body = do(t, r, http.MethodGet, cp2c1, ""); body["setpoint_kw"] != 300.0 {
		t.Fatalf("second car after first leaves: %v", body)
	}

	_, body = do(t, r, http.MethodGet, "/status", "")
	if body["allocated_kw"] != 300.0 || body["headroom_kw"] != 100.0 || body["active_sessions"] != 1.0 {
		t.Fatalf("status totals: %v", body)
	}
}

func TestAPI_UpdateWithCurve(t *testing.T) {
	r := newTestRouter(t)
	do(t, r, http.MethodPost, cp1c1+"/session", `{"vehicle_max_power_kw": 300}`)
	code, body := do(t, r, http.MethodPost, cp2c1+"/session", `{
		"vehicle_max_power_kw": 150,
		"soc_pct": 20,
		"charging_curve": [{"soc_pct":0,"max_power_kw":150},{"soc_pct":80,"max_power_kw":50},{"soc_pct":100,"max_power_kw":10}]
	}`)
	if code != http.StatusCreated {
		t.Fatalf("start with curve: %d %v", code, body)
	}
	// SoC 20 → 150 - 20/80*100 = 125 kW
	if body["demand_kw"] != 125.0 || body["setpoint_kw"] != 125.0 {
		t.Fatalf("curve demand: %v", body)
	}
	code, body = do(t, r, http.MethodPatch, cp2c1+"/session", `{"soc_pct": 90}`)
	if code != http.StatusOK || body["setpoint_kw"] != 30.0 {
		t.Fatalf("taper at 90%%: %d %v", code, body)
	}
	if _, body = do(t, r, http.MethodGet, cp1c1, ""); body["setpoint_kw"] != 300.0 {
		t.Fatalf("other car should take freed power up to its charger cap: %v", body)
	}
}

func TestAPI_Errors(t *testing.T) {
	r := newTestRouter(t)
	do(t, r, http.MethodPost, cp1c1+"/session", `{"vehicle_max_power_kw": 100}`)

	cases := []struct {
		name, method, path, body string
		want                     int
	}{
		{"malformed JSON", "POST", "/chargers/CP1/connectors/2/session", `{"vehicle_max_power_kw":`, 400},
		{"empty body", "POST", "/chargers/CP1/connectors/2/session", ``, 400},
		{"unknown field", "POST", "/chargers/CP1/connectors/2/session", `{"vehicle_max_power_kw": 1, "colour": "red"}`, 400},
		{"missing vehicle max", "POST", "/chargers/CP1/connectors/2/session", `{"soc_pct": 10}`, 400},
		{"negative vehicle max", "POST", "/chargers/CP1/connectors/2/session", `{"vehicle_max_power_kw": -5}`, 400},
		{"soc out of range", "POST", "/chargers/CP1/connectors/2/session", `{"vehicle_max_power_kw": 1, "soc_pct": 150}`, 400},
		{"unsorted curve", "POST", "/chargers/CP1/connectors/2/session",
			`{"vehicle_max_power_kw": 1, "charging_curve": [{"soc_pct":50,"max_power_kw":1},{"soc_pct":10,"max_power_kw":1}]}`, 400},
		{"unknown charger", "POST", "/chargers/NOPE/connectors/1/session", `{"vehicle_max_power_kw": 1}`, 404},
		{"unknown connector", "POST", "/chargers/CP1/connectors/9/session", `{"vehicle_max_power_kw": 1}`, 404},
		{"connector occupied", "POST", cp1c1 + "/session", `{"vehicle_max_power_kw": 1}`, 409},
		{"empty update", "PATCH", cp1c1 + "/session", `{}`, 400},
		{"update without session", "PATCH", cp2c1 + "/session", `{"soc_pct": 10}`, 404},
		{"stop without session", "DELETE", cp2c1 + "/session", ``, 404},
		{"bad charger status", "POST", "/chargers/CP1/status", `{"status": "broken"}`, 400},
		{"unknown charger status", "POST", "/chargers/NOPE/status", `{"status": "faulted"}`, 404},
		{"unknown charger read", "GET", "/chargers/NOPE", ``, 404},
		{"stream unknown charger", "GET", "/setpoints/stream?charger_id=NOPE", ``, 404},
		{"unknown route", "GET", "/nope", ``, 404},
		{"wrong method", "PUT", cp1c1 + "/session", `{}`, 405},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := do(t, r, tc.method, tc.path, tc.body)
			if code != tc.want {
				t.Fatalf("got %d %v, want %d", code, body, tc.want)
			}
			if msg, _ := body["error"].(string); msg == "" {
				t.Fatalf("error response must carry an error message, got %v", body)
			}
		})
	}
}

func TestAPI_ChargerFaultReturnsConflictForNewSessions(t *testing.T) {
	r := newTestRouter(t)
	do(t, r, http.MethodPost, cp1c1+"/session", `{"vehicle_max_power_kw": 300}`)
	do(t, r, http.MethodPost, cp2c1+"/session", `{"vehicle_max_power_kw": 300}`)

	code, body := do(t, r, http.MethodPost, "/chargers/CP1/status", `{"status": "faulted"}`)
	if code != http.StatusOK || body["status"] != "faulted" || body["allocated_kw"] != 0.0 {
		t.Fatalf("fault: %d %v", code, body)
	}
	if code, _ := do(t, r, http.MethodPost, "/chargers/CP1/connectors/2/session", `{"vehicle_max_power_kw": 50}`); code != http.StatusConflict {
		t.Fatalf("session on faulted charger: got %d, want 409", code)
	}
	if _, body = do(t, r, http.MethodGet, cp2c1, ""); body["setpoint_kw"] != 300.0 {
		t.Fatalf("healthy charger should take the freed power: %v", body)
	}
}

// A car connecting on CP2 must push CP1's lowered setpoint over SSE, with no request from CP1.
func TestAPI_SSEPushesSetpointsToOtherChargers(t *testing.T) {
	srv := httptest.NewServer(newTestRouter(t))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/setpoints/stream?charger_id=CP1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	events := readSSE(resp.Body)

	// Snapshot: one event per CP1 connector, all at 0.
	for range 2 {
		ev := nextEvent(t, events)
		if ev.name != "snapshot" || ev.data.ChargerID != "CP1" || ev.data.SetpointKW != 0 {
			t.Fatalf("unexpected snapshot event %+v", ev)
		}
	}

	post(t, srv.URL+cp1c1+"/session", `{"vehicle_max_power_kw": 350}`)
	if ev := nextEvent(t, events); ev.name != "setpoint" || ev.data.ConnectorID != "1" || ev.data.SetpointKW != 300 {
		t.Fatalf("CP1/1 should be pushed 300, got %+v", ev)
	}

	post(t, srv.URL+cp2c1+"/session", `{"vehicle_max_power_kw": 350}`) // event on CP2 …
	ev := nextEvent(t, events)                                         // … pushed to CP1
	if ev.data.ChargerID != "CP1" || ev.data.SetpointKW != 200 {
		t.Fatalf("CP1/1 should be pushed 200 after CP2 car arrives, got %+v", ev)
	}
	if ev.id != fmt.Sprint(ev.data.AllocationSeq) {
		t.Fatalf("SSE id (%s) must equal allocation_seq (%d)", ev.id, ev.data.AllocationSeq)
	}
}

// Measures full per-event handling latency against the <1 s requirement.
func TestAPI_EventLatency(t *testing.T) {
	r := newTestRouter(t)
	paths := []string{cp1c1, "/chargers/CP1/connectors/2", cp2c1, "/chargers/CP2/connectors/2"}

	var durations []time.Duration
	for i := range 1000 {
		p := paths[i%len(paths)]
		var method, body string
		switch (i / len(paths)) % 3 {
		case 0:
			method, body = http.MethodPost, fmt.Sprintf(`{"vehicle_max_power_kw": %d}`, 50+i%250)
		case 1:
			method, body = http.MethodPatch, fmt.Sprintf(`{"soc_pct": %d}`, i%100)
		case 2:
			method = http.MethodDelete
		}
		start := time.Now()
		code, resp := do(t, r, method, p+"/session", body)
		durations = append(durations, time.Since(start))
		if code >= 300 {
			t.Fatalf("event %d (%s %s): %d %v", i, method, p, code, resp)
		}
	}
	slices.Sort(durations)
	p50, p99, worst := durations[len(durations)/2], durations[len(durations)*99/100], durations[len(durations)-1]
	t.Logf("event latency over %d events: p50=%v p99=%v max=%v", len(durations), p50, p99, worst)
	if p99 > 50*time.Millisecond {
		t.Fatalf("p99 latency %v exceeds 50ms budget (requirement: <1s)", p99)
	}
}

// --- SSE test helpers ---

type sseEvent struct {
	name, id string
	data     station.SetpointEvent
}

func readSSE(body io.Reader) <-chan sseEvent {
	out := make(chan sseEvent, 16)
	go func() {
		defer close(out)
		sc := bufio.NewScanner(body)
		var ev sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event:"):
				ev.name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "id:"):
				ev.id = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
			case strings.HasPrefix(line, "data:"):
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data:")), &ev.data)
			case line == "" && ev.name != "":
				out <- ev
				ev = sseEvent{}
			}
		}
	}()
	return out
}

func nextEvent(t *testing.T, ch <-chan sseEvent) sseEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("SSE stream closed")
		}
		return ev
	case <-time.After(time.Second):
		t.Fatal("no SSE event within 1s")
	}
	return sseEvent{}
}

func post(t *testing.T, url, body string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST %s: %d", url, resp.StatusCode)
	}
}
