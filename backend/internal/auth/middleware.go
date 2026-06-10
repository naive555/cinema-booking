package auth

import (
	"cinema-booking/backend/config"
	"cinema-booking/backend/internal/domain"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const claimsKey = "auth.claims"

// Middleware extracts and validates the Bearer JWT.
// Token source priority:
//  1. Authorization: Bearer <token> header   — used by all REST requests
//  2. ?token=<token> query param             — used by WebSocket clients, because
//     the browser WebSocket API cannot send custom headers during the HTTP upgrade.
//
// On success it stores the verified *Claims under claimsKey for downstream handlers.
func Middleware(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := ""
		if h := c.GetHeader("Authorization"); strings.HasPrefix(h, "Bearer ") {
			raw = strings.TrimPrefix(h, "Bearer ")
		} else if q := c.Query("token"); q != "" {
			raw = q
		}
		if raw == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
			return
		}
		claims, err := ParseToken(raw, cfg)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token: " + err.Error()})
			return
		}
		c.Set(claimsKey, claims)
		c.Next()
	}
}

// RequireRole aborts with 403 if the authenticated user does not hold the given role.
// Must be chained after Middleware.
func RequireRole(role domain.Role) gin.HandlerFunc {
	return func(c *gin.Context) {
		claims, ok := c.MustGet(claimsKey).(*Claims)
		if !ok || claims.Role != role {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "insufficient permissions"})
			return
		}
		c.Next()
	}
}

// ClaimsFromContext retrieves the validated claims stored by Middleware.
func ClaimsFromContext(c *gin.Context) (*Claims, bool) {
	v, ok := c.Get(claimsKey)
	if !ok {
		return nil, false
	}
	claims, ok := v.(*Claims)
	return claims, ok
}
