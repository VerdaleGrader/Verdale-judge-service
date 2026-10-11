package routes

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"

	controllers_jobs "github.com/kanonkc/verdale-judge-service/controllers/jobs"
	controllers_system "github.com/kanonkc/verdale-judge-service/controllers/system"
	apperrors "github.com/kanonkc/verdale-judge-service/errors"
	"github.com/kanonkc/verdale-judge-service/pkg/hmacauth"
	"github.com/kanonkc/verdale-judge-service/pkg/metrics"
	"github.com/kanonkc/verdale-judge-service/pkg/response"
	"github.com/kanonkc/verdale-judge-service/pkg/rest_api"
	services_jobs "github.com/kanonkc/verdale-judge-service/services/jobs"
)

type Dependencies struct {
	Logger       *zap.Logger
	Metrics      *metrics.Metrics
	Verifier     hmacauth.Verifier
	MaxBodyBytes int64
	JobService   services_jobs.Service
	Version      string
	ReadyChecks  map[string]controllers_system.Check
}

// Setup builds the HTTP router. Judge only listens on the internal network:
// /internal/* is for Core (HMAC-signed), /metrics for monitoring.
func Setup(deps Dependencies) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(rest_api.RequestID(deps.Logger), rest_api.Recovery(), rest_api.AccessLog())
	router.NoRoute(func(ctx *gin.Context) {
		response.Error(ctx, apperrors.New(http.StatusNotFound, apperrors.CodeNotFound, "Not found."))
	})
	router.HandleMethodNotAllowed = true
	router.NoMethod(func(ctx *gin.Context) {
		response.Error(ctx, apperrors.New(http.StatusMethodNotAllowed, apperrors.CodeNotFound, "Method not allowed."))
	})

	systemCtrl := controllers_system.New(deps.Version, deps.ReadyChecks)
	router.GET("/health", systemCtrl.Health)
	router.GET("/ready", systemCtrl.Ready)
	router.GET("/metrics", gin.WrapH(promhttp.HandlerFor(deps.Metrics.Registry, promhttp.HandlerOpts{})))

	jobsCtrl := controllers_jobs.New(deps.JobService)
	internal := router.Group("/internal", rest_api.VerifySignature(deps.Verifier, deps.MaxBodyBytes))
	internal.POST("/jobs", jobsCtrl.Create)
	internal.GET("/jobs/:job_id", jobsCtrl.Get)
	return router
}
