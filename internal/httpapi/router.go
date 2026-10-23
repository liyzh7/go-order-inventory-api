// Package httpapi wires HTTP routes to their handlers.
package httpapi

import (
	"github.com/gin-gonic/gin"

	"internshiplab/order-api/internal/auth"
	"internshiplab/order-api/internal/handler"
	"internshiplab/order-api/internal/order"
	"internshiplab/order-api/internal/platform/securitylog"
	"internshiplab/order-api/internal/product"
)

// NewRouter creates the application's HTTP handler without starting a network
// listener, keeping route wiring independently testable.
func NewRouter(productHandler *product.Handler, orderHandler *order.Handler, authService *auth.Service) *gin.Engine {
	router := gin.New()
	router.Use(securitylog.HTTP())

	handler.RegisterHealthRoutes(router)
	productHandler.RegisterRoutes(router)
	auth.NewHandler(authService).RegisterRoutes(router)
	orderHandler.RegisterRoutes(router, auth.Middleware(authService))

	return router
}
