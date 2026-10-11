package jobs

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	apperrors "github.com/kanonkc/verdale-judge-service/errors"
	"github.com/kanonkc/verdale-judge-service/pkg/contract"
	"github.com/kanonkc/verdale-judge-service/pkg/logger"
	"github.com/kanonkc/verdale-judge-service/pkg/response"
	"github.com/kanonkc/verdale-judge-service/pkg/rest_api"
	service "github.com/kanonkc/verdale-judge-service/services/jobs"
)

type Controller struct {
	service service.Service
}

func New(service service.Service) *Controller {
	return &Controller{service: service}
}

// Create enqueues a job. Unknown JSON fields are ignored on purpose so Core
// can add optional fields before Judge knows them (contract change policy).
// POST /internal/jobs
func (c *Controller) Create(ctx *gin.Context) {
	if contentType := ctx.ContentType(); contentType != "application/json" {
		response.Error(ctx, apperrors.New(http.StatusUnsupportedMediaType, apperrors.CodeUnsupportedMedia, "Content-Type must be application/json."))
		return
	}
	var request contract.JobRequest
	if err := json.Unmarshal(ctx.MustGet(rest_api.ContextRawBody).([]byte), &request); err != nil {
		response.Error(ctx, apperrors.New(http.StatusBadRequest, apperrors.CodeInvalidJSON, "The body is not a valid job request: "+err.Error()))
		return
	}
	accepted, err := c.service.Accept(ctx.Request.Context(), request)
	if err != nil {
		response.Error(ctx, err)
		return
	}
	response.Logger(ctx).Info("job accepted", zap.String(logger.FieldJobID, request.JobId.String()),
		zap.Bool("duplicate", accepted.Duplicate), zap.String("status", string(accepted.Status)))
	ctx.JSON(http.StatusAccepted, accepted)
}

// Get returns the state of a job.
// GET /internal/jobs/:job_id
func (c *Controller) Get(ctx *gin.Context) {
	jobID, err := uuid.Parse(ctx.Param("job_id"))
	if err != nil {
		response.Error(ctx, apperrors.Validation([]contract.ErrorDetail{{Field: "job_id", Message: "must be a UUID"}}))
		return
	}
	state, err := c.service.Get(ctx.Request.Context(), jobID)
	if err != nil {
		response.Error(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, state)
}
