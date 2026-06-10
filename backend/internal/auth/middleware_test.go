package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cinema-booking/backend/config"
	"cinema-booking/backend/internal/auth"
	"cinema-booking/backend/internal/domain"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func init() { gin.SetMode(gin.TestMode) }

func minimalCfg() *config.Config {
	return &config.Config{JWTSecret: "test-secret-32-bytes-long-padded!", JWTExpiry: 24 * time.Hour}
}

func mintFor(t *testing.T, role domain.Role) string {
	t.Helper()
	tok, err := auth.MintToken(&domain.User{
		ID:    bson.NewObjectID(),
		Email: "test@example.com",
		Role:  role,
	}, minimalCfg())
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	return tok
}

// routerWith builds a minimal Gin router with the given middleware chain.
func routerWith(handlers ...gin.HandlerFunc) *gin.Engine {
	r := gin.New()
	r.GET("/probe", handlers...)
	return r
}

func do(r *gin.Engine, token string) int {
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

// ── Middleware ────────────────────────────────────────────────────────────────

func TestMiddleware_NoToken_Returns401(t *testing.T) {
	cfg := minimalCfg()
	r := routerWith(auth.Middleware(cfg), func(c *gin.Context) { c.Status(200) })
	if got := do(r, ""); got != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", got)
	}
}

func TestMiddleware_GarbageToken_Returns401(t *testing.T) {
	cfg := minimalCfg()
	r := routerWith(auth.Middleware(cfg), func(c *gin.Context) { c.Status(200) })
	if got := do(r, "not.a.jwt"); got != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", got)
	}
}

func TestMiddleware_WrongSecret_Returns401(t *testing.T) {
	// Token minted with one secret, verified with another.
	otherCfg := &config.Config{JWTSecret: "different-secret", JWTExpiry: time.Hour}
	tok, _ := auth.MintToken(&domain.User{ID: bson.NewObjectID(), Email: "x@x.com", Role: domain.RoleUser}, otherCfg)

	cfg := minimalCfg()
	r := routerWith(auth.Middleware(cfg), func(c *gin.Context) { c.Status(200) })
	if got := do(r, tok); got != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", got)
	}
}

func TestMiddleware_ValidToken_Returns200(t *testing.T) {
	cfg := minimalCfg()
	tok := mintFor(t, domain.RoleUser)
	r := routerWith(auth.Middleware(cfg), func(c *gin.Context) { c.Status(200) })
	if got := do(r, tok); got != http.StatusOK {
		t.Errorf("want 200, got %d", got)
	}
}

// ── RequireRole ───────────────────────────────────────────────────────────────

func TestRequireRole_UserOnAdminRoute_Returns403(t *testing.T) {
	cfg := minimalCfg()
	tok := mintFor(t, domain.RoleUser)
	r := routerWith(
		auth.Middleware(cfg),
		auth.RequireRole(domain.RoleAdmin),
		func(c *gin.Context) { c.Status(200) },
	)
	if got := do(r, tok); got != http.StatusForbidden {
		t.Errorf("want 403, got %d", got)
	}
}

func TestRequireRole_AdminOnAdminRoute_Returns200(t *testing.T) {
	cfg := minimalCfg()
	tok := mintFor(t, domain.RoleAdmin)
	r := routerWith(
		auth.Middleware(cfg),
		auth.RequireRole(domain.RoleAdmin),
		func(c *gin.Context) { c.Status(200) },
	)
	if got := do(r, tok); got != http.StatusOK {
		t.Errorf("want 200, got %d", got)
	}
}

func TestRequireRole_AdminOnUserRoute_Returns200(t *testing.T) {
	cfg := minimalCfg()
	tok := mintFor(t, domain.RoleAdmin)
	r := routerWith(
		auth.Middleware(cfg),
		auth.RequireRole(domain.RoleUser),
		func(c *gin.Context) { c.Status(200) },
	)
	// ADMIN does not satisfy RequireRole(USER) — roles are exact, not hierarchical
	if got := do(r, tok); got != http.StatusForbidden {
		t.Errorf("want 403 (roles are exact, not hierarchical), got %d", got)
	}
}
