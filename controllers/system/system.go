package system

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kanonkc/verdale-judge-service/pkg/contract"
)

// Check reports whether one dependency is ready.
type Check func(ctx context.Context) bool

type Controller struct {
	version string
	checks  map[string]Check
}

func New(version string, checks map[string]Check) *Controller {
	return &Controller{version: version, checks: checks}
}

// Health is liveness: the process is up.
// GET /health
func (c *Controller) Health(ctx *gin.Context) {
	ctx.JSON(http.StatusOK, contract.HealthResponse{Status: contract.HealthResponseStatusOk, Version: c.version})
}

// Ready is readiness: Redis answers and the sandbox environment passed its
// startup check.
// GET /ready
func (c *Controller) Ready(ctx *gin.Context) {
	checkCtx, cancel := context.WithTimeout(ctx.Request.Context(), 2*time.Second)
	defer cancel()
	body := contract.ReadinessResponse{Status: contract.ReadinessResponseStatusReady, Checks: map[string]bool{}}
	for name, check := range c.checks {
		ok := check(checkCtx)
		body.Checks[name] = ok
		if !ok {
			body.Status = contract.ReadinessResponseStatusNotReady
		}
	}
	status := http.StatusOK
	if body.Status != contract.ReadinessResponseStatusReady {
		status = http.StatusServiceUnavailable
	}
	ctx.JSON(status, body)
}
