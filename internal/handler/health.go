package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// HealthResponse is the JSON contract returned by the health endpoint.
type HealthResponse struct {
	Status string `json:"status"`
}

// RegisterHealthRoutes keeps route wiring close to the handler it exposes.
func RegisterHealthRoutes(router gin.IRoutes) {
	router.GET("/health", GetHealth)
}

// GetHealth reports that the process is running and able to serve requests.
func GetHealth(c *gin.Context) {
	c.JSON(http.StatusOK, HealthResponse{Status: "ok"})
}
