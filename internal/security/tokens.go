package security

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type Claims struct {
	UserID uuid.UUID
	Role   string
}

type TokenManager struct {
	secret []byte
	issuer string
	ttl    time.Duration
}

func NewTokenManager(secret, issuer string, ttl time.Duration) *TokenManager {
	return &TokenManager{secret: []byte(secret), issuer: issuer, ttl: ttl}
}

func (m *TokenManager) TTL() time.Duration { return m.ttl }

type jwtClaims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}

func (m *TokenManager) IssueAccess(userID uuid.UUID, role string) (string, time.Time, error) {
	now := time.Now()
	exp := now.Add(m.ttl)
	c := jwtClaims{Role: role, RegisteredClaims: jwt.RegisteredClaims{
		Issuer: m.issuer, Subject: userID.String(), IssuedAt: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(exp), ID: uuid.NewString(),
	}}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(m.secret)
	return s, exp, err
}

var ErrInvalidToken = errors.New("invalid or expired token")

func (m *TokenManager) VerifyAccess(token string) (Claims, error) {
	var c jwtClaims
	t, err := jwt.ParseWithClaims(token, &c, func(*jwt.Token) (any, error) { return m.secret, nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer(m.issuer), jwt.WithExpirationRequired())
	if err != nil || !t.Valid {
		return Claims{}, ErrInvalidToken
	}
	id, err := uuid.Parse(c.Subject)
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	return Claims{UserID: id, Role: c.Role}, nil
}

// NewRefreshToken returns an opaque random token and its SHA-256 hash (the only form stored).
func NewRefreshToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
