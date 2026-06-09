package auth

import (
	"cinema-booking/backend/config"
	"cinema-booking/backend/internal/domain"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims is the payload embedded in every JWT this service issues.
//
//	{
//	  "sub":   "<mongo user ObjectID hex>",
//	  "email": "user@example.com",
//	  "role":  "USER" | "ADMIN",
//	  "iat":   <unix>,
//	  "exp":   <unix>
//	}
type Claims struct {
	Email string      `json:"email"`
	Role  domain.Role `json:"role"`
	jwt.RegisteredClaims
}

// MintToken creates a signed HS256 JWT for the given user.
func MintToken(user *domain.User, cfg *config.Config) (string, error) {
	now := time.Now()
	claims := Claims{
		Email: user.Email,
		Role:  user.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID.Hex(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(cfg.JWTExpiry)),
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return t.SignedString([]byte(cfg.JWTSecret))
}

// ParseToken validates the token string and returns its claims.
func ParseToken(tokenStr string, cfg *config.Config) (*Claims, error) {
	t, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return []byte(cfg.JWTSecret), nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := t.Claims.(*Claims)
	if !ok || !t.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}
	return claims, nil
}
