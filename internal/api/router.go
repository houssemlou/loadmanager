// Package api exposes the station over HTTP (Gin). It is a thin adapter:
// request decoding/validation and error → status mapping only. All domain
// rules live in the station package.
package api

import (
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"

	"github.com/houssemlou/loadmanager/internal/station"
)

// NewRouter builds the HTTP handler for a station.
func NewRouter(st *station.Station, log *slog.Logger) *gin.Engine {
	configureBinding()

	r := gin.New()
	r.HandleMethodNotAllowed = true
	r.Use(gin.Recovery(), requestLogger(log))

	h := &handler{st: st}

	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	r.GET("/config", h.getConfig)
	r.GET("/status", h.getStatus)
	r.GET("/setpoints/stream", h.streamSetpoints)

	charger := r.Group("/chargers/:cid")
	charger.GET("", h.getCharger)
	charger.POST("/status", h.setChargerStatus)

	connector := charger.Group("/connectors/:n")
	connector.GET("", h.getConnector)
	connector.POST("/session", h.startSession)
	connector.PATCH("/session", h.updateSession)
	connector.DELETE("/session", h.stopSession)

	r.NoRoute(func(c *gin.Context) { c.JSON(http.StatusNotFound, errorBody{Error: "route not found"}) })
	r.NoMethod(func(c *gin.Context) { c.JSON(http.StatusMethodNotAllowed, errorBody{Error: "method not allowed"}) })
	return r
}

// configureBinding makes JSON binding strict (unknown fields rejected) and
// reports validation errors with JSON field names instead of Go names.
func configureBinding() {
	binding.EnableDecoderDisallowUnknownFields = true
	if v, ok := binding.Validator.Engine().(*validator.Validate); ok {
		v.RegisterTagNameFunc(func(f reflect.StructField) string {
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				return f.Name
			}
			return name
		})
	}
}

func requestLogger(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.Info("http",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"duration_us", time.Since(start).Microseconds())
	}
}
