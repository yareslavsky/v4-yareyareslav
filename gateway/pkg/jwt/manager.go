package jwt

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	AccessTTL  = 5 * time.Minute
	RefreshTTL = 7 * 24 * time.Hour
)

type Manager struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
}

func NewManager(secret string, accessTTL time.Duration, refreshTTL time.Duration) *Manager {
	return &Manager{
		secret:     []byte(secret),
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
	}
}

type Claims struct {
	Role      string `json:"role"`
	TokenType string `json:"typ"`

	jwt.RegisteredClaims
}

func (c Claims) Validate() error {
	if _, err := uuid.Parse(c.Subject); err != nil || len(c.Subject) != 36 {
		return errors.New("invalid subject")
	}
	if c.Role != "buyer" && c.Role != "seller" {
		return errors.New("invalid role")
	}
	if c.TokenType != "access" && c.TokenType != "refresh" {
		return errors.New("invalid token type")
	}
	if c.IssuedAt == nil {
		return errors.New("issued-at claim is required")
	}
	return nil
}

func (m *Manager) generateToken(userID string, role string, tokenType string, ttl time.Duration) (string, error) {
	now := time.Now()

	claims := Claims{
		Role:      role,
		TokenType: tokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	return token.SignedString(m.secret)
}

func (m *Manager) GenerateAccessToken(userID string, role string) (string, error) {
	return m.generateToken(userID, role, "access", m.accessTTL)
}

func (m *Manager) GenerateRefreshToken(userID string, role string) (string, error) {
	return m.generateToken(userID, role, "refresh", m.refreshTTL)
}

func (m *Manager) Parse(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(
		tokenString,
		&Claims{},
		func(token *jwt.Token) (any, error) {
			return m.secret, nil
		},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	)

	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token")
	}

	return claims, nil
}
