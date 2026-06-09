package auth

import (
	"cinema-booking/backend/config"
	"cinema-booking/backend/internal/domain"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const stateCookie = "oauth_state"

// Handler holds the dependencies for auth HTTP handlers.
type Handler struct {
	cfg         *config.Config
	db          *mongo.Database
	oauthConfig *oauth2.Config
}

// NewHandler builds an auth Handler wired to the given config and database.
func NewHandler(cfg *config.Config, db *mongo.Database) *Handler {
	return &Handler{
		cfg: cfg,
		db:  db,
		oauthConfig: &oauth2.Config{
			ClientID:     cfg.GoogleClientID,
			ClientSecret: cfg.GoogleClientSecret,
			RedirectURL:  cfg.GoogleRedirectURL,
			Scopes:       []string{"openid", "email", "profile"},
			Endpoint:     google.Endpoint,
		},
	}
}

// Login redirects the browser to Google's OAuth consent screen.
// It sets a short-lived httpOnly state cookie for CSRF protection.
func (h *Handler) Login(c *gin.Context) {
	state, err := randomHex(16)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "state generation failed"})
		return
	}
	c.SetCookie(stateCookie, state, 300, "/", "", false, true)
	c.Redirect(http.StatusFound, h.oauthConfig.AuthCodeURL(state, oauth2.AccessTypeOnline))
}

// Callback handles Google's redirect back.
// It validates the CSRF state, exchanges the code for tokens, fetches the Google
// profile, upserts the user in Mongo, mints a JWT, and returns {"token": "..."}.
func (h *Handler) Callback(c *gin.Context) {
	cookieState, err := c.Cookie(stateCookie)
	if err != nil || cookieState != c.Query("state") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid oauth state"})
		return
	}
	c.SetCookie(stateCookie, "", -1, "/", "", false, true) // clear after use

	code := c.Query("code")
	if code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing authorization code"})
		return
	}

	oauthToken, err := h.oauthConfig.Exchange(c.Request.Context(), code)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "code exchange failed"})
		return
	}

	profile, err := fetchGoogleProfile(c.Request.Context(), h.oauthConfig.Client(c.Request.Context(), oauthToken))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "profile fetch failed"})
		return
	}

	role := h.resolveRole(profile.Email)

	user, err := h.upsertUser(c.Request.Context(), profile.Email, profile.Name, role)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "user persistence failed"})
		return
	}

	tokenStr, err := MintToken(user, h.cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "token mint failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": tokenStr})
}

// Me returns the authenticated caller's identity from the JWT claims.
func (h *Handler) Me(c *gin.Context) {
	claims, ok := ClaimsFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "no auth context"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id":    claims.Subject,
		"email": claims.Email,
		"role":  claims.Role,
	})
}

// DevToken mints a JWT without an OAuth round-trip.
// Only registered when DEV_AUTH=true. Never enabled in production.
func (h *Handler) DevToken(c *gin.Context) {
	var req struct {
		Email string      `json:"email" binding:"required,email"`
		Role  domain.Role `json:"role"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Role == "" {
		req.Role = domain.RoleUser
	}
	if req.Role != domain.RoleUser && req.Role != domain.RoleAdmin {
		c.JSON(http.StatusBadRequest, gin.H{"error": `role must be "USER" or "ADMIN"`})
		return
	}

	user := &domain.User{
		ID:    bson.NewObjectID(),
		Email: req.Email,
		Name:  "Dev User",
		Role:  req.Role,
	}
	tokenStr, err := MintToken(user, h.cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "token mint failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": tokenStr})
}

// ── helpers ───────────────────────────────────────────────────────────────────

type googleProfile struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

func fetchGoogleProfile(ctx context.Context, client *http.Client) (*googleProfile, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.googleapis.com/oauth2/v2/userinfo", nil)
	if err != nil {
		return nil, fmt.Errorf("build userinfo request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("userinfo request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("userinfo API returned %d: %s", resp.StatusCode, body)
	}
	var p googleProfile
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, fmt.Errorf("decode userinfo: %w", err)
	}
	if p.Email == "" {
		return nil, fmt.Errorf("userinfo missing email field")
	}
	return &p, nil
}

func (h *Handler) resolveRole(email string) domain.Role {
	for _, admin := range h.cfg.AdminEmails {
		if strings.EqualFold(admin, email) {
			return domain.RoleAdmin
		}
	}
	return domain.RoleUser
}

func (h *Handler) upsertUser(ctx context.Context, email, name string, role domain.Role) (*domain.User, error) {
	update := bson.M{
		"$setOnInsert": bson.M{"createdAt": time.Now()},
		"$set":         bson.M{"email": email, "name": name, "role": role},
	}
	opts := options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)
	var user domain.User
	err := h.db.Collection("users").FindOneAndUpdate(ctx, bson.M{"email": email}, update, opts).Decode(&user)
	if err != nil {
		return nil, fmt.Errorf("upsert user %q: %w", email, err)
	}
	return &user, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
