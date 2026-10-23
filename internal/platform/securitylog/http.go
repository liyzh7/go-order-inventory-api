package securitylog

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// HTTP generates correlation IDs instead of trusting a user-supplied header.
// Its recovery deliberately avoids Gin's request/stack/panic-value dump, all of
// which may contain secrets. Security events log at their business boundary.
func HTTP() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, err := NewContext(c.Request.Context())
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "internal_error", "message": "internal server error"}})
			return
		}
		c.Request = c.Request.WithContext(ctx)
		c.Header("X-Request-ID", RequestID(ctx))
		defer func() {
			if recover() != nil {
				Log(c.Request.Context(), "http.panic", "failure", 500, UserID(c.Request.Context()), "internal_error")
				if !c.Writer.Written() {
					c.AbortWithStatusJSON(500, gin.H{"error": gin.H{"code": "internal_error", "message": "internal server error"}})
				} else {
					c.Abort()
				}
			}
		}()
		c.Next()
	}
}
