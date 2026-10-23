package product

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"internshiplab/order-api/internal/platform/securitylog"
)

type productService interface {
	List(ctx context.Context, params ListParams) (ListResult, error)
	Get(ctx context.Context, id uint64) (Product, error)
}

type Handler struct {
	service productService
}

func NewHandler(service productService) *Handler {
	return &Handler{service: service}
}

func (h *Handler) RegisterRoutes(router gin.IRoutes) {
	router.GET("/products", h.list)
	router.GET("/products/:id", h.get)
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

type listData struct {
	Items      []Product          `json:"items"`
	Pagination paginationResponse `json:"pagination"`
}

type paginationResponse struct {
	Limit       int     `json:"limit"`
	AfterID     uint64  `json:"after_id"`
	NextAfterID *uint64 `json:"next_after_id"`
	HasMore     bool    `json:"has_more"`
}

func (h *Handler) list(c *gin.Context) {
	params, err := parseListParams(c)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}

	result, err := h.service.List(c.Request.Context(), params)
	if err != nil {
		writeServiceError(c, err)
		return
	}

	c.JSON(http.StatusOK, dataResponse{Data: listData{
		Items: result.Products,
		Pagination: paginationResponse{
			Limit:       params.Limit,
			AfterID:     params.AfterID,
			NextAfterID: result.NextAfterID,
			HasMore:     result.HasMore,
		},
	}})
}

func (h *Handler) get(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		writeError(c, http.StatusBadRequest, "invalid_argument", "product id must be a positive integer")
		return
	}

	found, err := h.service.Get(c.Request.Context(), id)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, dataResponse{Data: found})
}

func parseListParams(c *gin.Context) (ListParams, error) {
	params := ListParams{Limit: DefaultPageSize}

	if rawLimit, exists := c.GetQuery("limit"); exists {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit < 1 || limit > MaxPageSize {
			return ListParams{}, errors.New("limit must be an integer between 1 and 100")
		}
		params.Limit = limit
	}

	if rawAfterID, exists := c.GetQuery("after_id"); exists {
		afterID, err := strconv.ParseUint(rawAfterID, 10, 64)
		if err != nil {
			return ListParams{}, errors.New("after_id must be a non-negative integer")
		}
		params.AfterID = afterID
	}

	return params, nil
}

func writeServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrInvalidArgument):
		writeError(c, http.StatusBadRequest, "invalid_argument", "invalid product request")
	case errors.Is(err, ErrNotFound):
		writeError(c, http.StatusNotFound, "not_found", "product not found")
	default:
		// Keep driver and SQL details out of the public response.
		securitylog.Log(c.Request.Context(), "product.operation", "failure", 500, securitylog.UserID(c.Request.Context()), "internal_error")
		writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

func writeError(c *gin.Context, status int, code, message string) {
	c.JSON(status, errorResponse{Error: apiError{Code: code, Message: message}})
}
