package order

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"internshiplab/order-api/internal/auth"
	"internshiplab/order-api/internal/platform/securitylog"
)

type orderService interface {
	Create(ctx context.Context, idempotencyKey string, request CreateRequest) (CreateResult, error)
	Get(ctx context.Context, id uint64) (Order, error)
	Cancel(ctx context.Context, id uint64) (CancelResult, error)
}

type Handler struct {
	service  orderService
	recorder StageRecorder
}

func NewHandler(service orderService) *Handler {
	return &Handler{service: service}
}

func NewHandlerWithRecorder(service orderService, recorder StageRecorder) *Handler {
	return &Handler{service: service, recorder: recorder}
}

func (h *Handler) RegisterRoutes(router gin.IRoutes, authenticate gin.HandlerFunc) {
	router.POST("/orders", authenticate, h.create)
	router.GET("/orders/:id", authenticate, h.get)
	router.POST("/orders/:id/cancel", authenticate, h.cancel)
}

func (h *Handler) get(c *gin.Context) {
	id, ok := parseOrderID(c)
	if !ok {
		return
	}
	found, err := h.service.Get(c.Request.Context(), id)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, dataResponse{Data: found})
}

func (h *Handler) cancel(c *gin.Context) {
	defer recordCancellation(c)
	id, ok := parseOrderID(c)
	if !ok {
		return
	}
	result, err := h.service.Cancel(c.Request.Context(), id)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	if result.Replayed {
		c.Header("Cancellation-Replayed", "true")
	}
	c.JSON(http.StatusOK, dataResponse{Data: result.Order})
}

func parseOrderID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		writeError(c, http.StatusBadRequest, "invalid_argument", "order id must be a positive integer")
		return 0, false
	}
	return id, true
}

func (h *Handler) create(c *gin.Context) {
	started := time.Now()
	outcome := StageOutcomeFailure
	defer func() { observeStage(h.recorder, StageHTTPCreateTotal, outcome, started) }()
	if err := requireJSONContentType(c.Request.Header.Get("Content-Type")); err != nil {
		outcome = StageOutcomeBusinessRejected
		writeError(c, http.StatusUnsupportedMediaType, "unsupported_media_type", err.Error())
		return
	}
	keys := c.Request.Header.Values("Idempotency-Key")
	if len(keys) != 1 {
		outcome = StageOutcomeBusinessRejected
		writeError(c, http.StatusBadRequest, "invalid_idempotency_key", "exactly one Idempotency-Key header is required")
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxRequestBytes)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var request CreateRequest
	if err := decoder.Decode(&request); err != nil {
		outcome = StageOutcomeBusinessRejected
		writeError(c, http.StatusBadRequest, "invalid_json", jsonErrorMessage(err))
		return
	}
	if err := ensureJSONEnd(decoder); err != nil {
		outcome = StageOutcomeBusinessRejected
		writeError(c, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	result, err := h.service.Create(c.Request.Context(), keys[0], request)
	if err != nil {
		if isBusinessRejection(err) {
			outcome = StageOutcomeBusinessRejected
		}
		writeServiceError(c, err)
		return
	}
	if result.Replayed {
		outcome = StageOutcomeReplay
		c.Header("Idempotency-Replayed", "true")
		c.JSON(http.StatusOK, dataResponse{Data: result.Order})
		return
	}
	outcome = StageOutcomeSuccess
	c.JSON(http.StatusCreated, dataResponse{Data: result.Order})
}

func requireJSONContentType(value string) error {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil || mediaType != "application/json" {
		return errors.New("Content-Type must be application/json")
	}
	return nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("request body must contain exactly one JSON object: %w", err)
	}
	return errors.New("request body must contain exactly one JSON object")
}

func jsonErrorMessage(err error) string {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return fmt.Sprintf("request body exceeds %d bytes", MaxRequestBytes)
	}
	return "request body must be one valid JSON object with only supported fields"
}

func writeServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, auth.ErrUnauthorized):
		writeError(c, 401, "unauthorized", "valid authentication required")
	case errors.Is(err, ErrInvalidIdempotency):
		writeError(c, http.StatusBadRequest, "invalid_idempotency_key", err.Error())
	case errors.Is(err, ErrInvalidArgument), errors.Is(err, ErrAmountOverflow):
		writeError(c, http.StatusBadRequest, "invalid_argument", err.Error())
	case errors.Is(err, ErrProductNotFound):
		writeError(c, http.StatusNotFound, "product_not_found", "one or more products do not exist")
	case errors.Is(err, ErrProductInactive):
		writeError(c, http.StatusConflict, "product_inactive", "one or more products are inactive")
	case errors.Is(err, ErrInsufficientStock):
		writeError(c, http.StatusConflict, "insufficient_stock", "one or more products have insufficient stock")
	case errors.Is(err, ErrIdempotencyConflict):
		writeError(c, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used for a different request")
	case errors.Is(err, ErrOrderNotFound):
		// Do not log the requested ID or whether a different user's row exists.
		securitylog.Log(c.Request.Context(), "order.access_denied", "failure", 404, securitylog.UserID(c.Request.Context()), "order_not_found")
		writeError(c, http.StatusNotFound, "order_not_found", "order not found")
	case errors.Is(err, ErrInvalidState):
		writeError(c, http.StatusConflict, "order_state_conflict", "order cannot be cancelled from its current state")
	case errors.Is(err, ErrInventoryOverflow):
		writeError(c, http.StatusConflict, "inventory_overflow", "order cannot be cancelled because inventory would overflow")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeError(c, http.StatusGatewayTimeout, "request_timeout", "request was cancelled or timed out")
	case errors.Is(err, ErrDatabaseUnavailable), errors.Is(err, ErrCommitUncertain):
		securitylog.Log(c.Request.Context(), "order.operation", "failure", 503, securitylog.UserID(c.Request.Context()), "database_error")
		writeError(c, http.StatusServiceUnavailable, "temporarily_unavailable", "operation result could not be confirmed; retry the same request")
	default:
		securitylog.Log(c.Request.Context(), "order.operation", "failure", 500, securitylog.UserID(c.Request.Context()), "internal_error")
		writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

type dataResponse struct {
	Data any `json:"data"`
}

type errorResponse struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(c *gin.Context, status int, code, message string) {
	c.Set("order_error_category", code)
	c.JSON(status, errorResponse{Error: apiError{Code: code, Message: message}})
}

func recordCancellation(c *gin.Context) {
	outcome, category := "success", "none"
	status := c.Writer.Status()
	if !c.Writer.Written() {
		status = 500
		outcome = "failure"
		category = "internal_error"
	}
	if status >= 400 {
		outcome = "failure"
		if value := c.GetString("order_error_category"); value != "" {
			category = value
		}
		if category == "temporarily_unavailable" {
			category = "database_error"
		}
		if category == "request_timeout" {
			category = "timeout"
		}
	}
	if outcome == "success" && c.Writer.Header().Get("Cancellation-Replayed") == "true" {
		outcome = "replayed"
	}
	securitylog.Log(c.Request.Context(), "order.cancel", outcome, status, securitylog.UserID(c.Request.Context()), category)
}
