package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-contrib/sse"
	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"

	"github.com/houssemlou/loadmanager/internal/station"
)

type handler struct {
	st *station.Station
}

type errorBody struct {
	Error string `json:"error"`
}

// --- request DTOs ----------------------------------------------------------

type startSessionRequest struct {
	VehicleMaxPowerKW float64              `json:"vehicle_max_power_kw" binding:"required,gt=0"`
	ChargingCurve     []station.CurvePoint `json:"charging_curve"`
	SoCPct            *float64             `json:"soc_pct" binding:"omitempty,gte=0,lte=100"`
	RequestedPowerKW  *float64             `json:"requested_power_kw" binding:"omitempty,gte=0"`
}

type updateSessionRequest struct {
	SoCPct           *float64 `json:"soc_pct" binding:"omitempty,gte=0,lte=100"`
	RequestedPowerKW *float64 `json:"requested_power_kw" binding:"omitempty,gte=0"`
}

type chargerStatusRequest struct {
	Status string `json:"status" binding:"required,oneof=available faulted offline"`
}

// --- read endpoints --------------------------------------------------------

func (h *handler) getConfig(c *gin.Context) { c.JSON(http.StatusOK, h.st.Config()) }

func (h *handler) getStatus(c *gin.Context) { c.JSON(http.StatusOK, h.st.Status()) }

func (h *handler) getCharger(c *gin.Context) {
	v, err := h.st.Charger(c.Param("cid"))
	respond(c, http.StatusOK, v, err)
}

func (h *handler) getConnector(c *gin.Context) {
	v, err := h.st.Connector(c.Param("cid"), c.Param("n"))
	respond(c, http.StatusOK, v, err)
}

// --- charger events --------------------------------------------------------

func (h *handler) setChargerStatus(c *gin.Context) {
	var req chargerStatusRequest
	if !bind(c, &req) {
		return
	}
	v, err := h.st.SetChargerStatus(c.Param("cid"), station.ChargerStatus(req.Status))
	respond(c, http.StatusOK, v, err)
}

func (h *handler) startSession(c *gin.Context) {
	var req startSessionRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.st.StartSession(c.Param("cid"), c.Param("n"), station.SessionStart{
		VehicleMaxPowerKW: req.VehicleMaxPowerKW,
		ChargingCurve:     req.ChargingCurve,
		SoCPct:            req.SoCPct,
		RequestedPowerKW:  req.RequestedPowerKW,
	})
	respond(c, http.StatusCreated, res, err)
}

func (h *handler) updateSession(c *gin.Context) {
	var req updateSessionRequest
	if !bind(c, &req) {
		return
	}
	if req.SoCPct == nil && req.RequestedPowerKW == nil {
		c.JSON(http.StatusBadRequest, errorBody{Error: "at least one of soc_pct, requested_power_kw is required"})
		return
	}
	res, err := h.st.UpdateSession(c.Param("cid"), c.Param("n"), station.SessionUpdate{
		SoCPct:           req.SoCPct,
		RequestedPowerKW: req.RequestedPowerKW,
	})
	respond(c, http.StatusOK, res, err)
}

func (h *handler) stopSession(c *gin.Context) {
	res, err := h.st.StopSession(c.Param("cid"), c.Param("n"))
	respond(c, http.StatusOK, res, err)
}

// --- setpoint stream (SSE) -------------------------------------------------

const sseHeartbeat = 15 * time.Second

// streamSetpoints pushes setpoint changes to chargers. It first sends a
// "snapshot" event per connector, then "setpoint" events with the latest value
// of each changed connector (a slow client skips stale intermediates). A
// comment line is sent periodically to keep proxies from closing the stream.
func (h *handler) streamSetpoints(c *gin.Context) {
	sub, snapshot, err := h.st.Subscribe(c.Query("charger_id"))
	if err != nil {
		writeError(c, err)
		return
	}
	defer h.st.Unsubscribe(sub)

	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	for _, ev := range snapshot {
		renderEvent(c, "snapshot", ev)
	}
	c.Writer.Flush()

	heartbeat := time.NewTicker(sseHeartbeat)
	defer heartbeat.Stop()
	ctx := c.Request.Context()

	c.Stream(func(w io.Writer) bool {
		select {
		case <-ctx.Done():
			return false
		case _, ok := <-sub.Notify():
			if !ok {
				return false
			}
			for _, ev := range sub.Drain() {
				renderEvent(c, "setpoint", ev)
			}
			return true
		case <-heartbeat.C:
			_, _ = io.WriteString(w, ": keepalive\n\n")
			return true
		}
	})
}

func renderEvent(c *gin.Context, name string, ev station.SetpointEvent) {
	c.Render(-1, sse.Event{Event: name, Id: strconv.FormatUint(ev.AllocationSeq, 10), Data: ev})
}

// --- helpers ---------------------------------------------------------------

// bind decodes the JSON body; on failure it writes a 400 and returns false.
func bind(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		c.JSON(http.StatusBadRequest, errorBody{Error: bindErrorMessage(err)})
		return false
	}
	return true
}

func bindErrorMessage(err error) string {
	var verrs validator.ValidationErrors
	if errors.As(err, &verrs) {
		msgs := make([]string, 0, len(verrs))
		for _, fe := range verrs {
			if fe.Param() != "" {
				msgs = append(msgs, fmt.Sprintf("%s must satisfy %s=%s", fe.Field(), fe.Tag(), fe.Param()))
			} else {
				msgs = append(msgs, fmt.Sprintf("%s is %s", fe.Field(), fe.Tag()))
			}
		}
		return strings.Join(msgs, "; ")
	}
	if errors.Is(err, io.EOF) {
		return "request body is required"
	}
	return "invalid JSON: " + err.Error()
}

func respond(c *gin.Context, okStatus int, body any, err error) {
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(okStatus, body)
}

func writeError(c *gin.Context, err error) {
	c.JSON(statusFor(err), errorBody{Error: err.Error()})
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, station.ErrInvalidInput):
		return http.StatusBadRequest
	case errors.Is(err, station.ErrUnknownCharger),
		errors.Is(err, station.ErrUnknownConnector),
		errors.Is(err, station.ErrNoSession):
		return http.StatusNotFound
	case errors.Is(err, station.ErrConnectorOccupied),
		errors.Is(err, station.ErrChargerUnavailable):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}
