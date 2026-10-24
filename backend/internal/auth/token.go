package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Principal is the authenticated caller.
type Principal struct {
	UserID     int64
	Role       Role
	CustomerID int64 // only for the customer role
	Email      string
}

type claims struct {
	Role       Role   `json:"role"`
	CustomerID int64  `json:"cid,omitempty"`
	Email      string `json:"email"`
	jwt.RegisteredClaims
}

// Issuer signs and verifies access tokens.
type Issuer struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// NewIssuer creates an HS256 issuer.
func NewIssuer(secret []byte, ttl time.Duration) *Issuer {
	return &Issuer{secret: secret, ttl: ttl, now: time.Now}
}

const issuer = "sogukzincir"

// ErrInvalidToken covers every reason a token is rejected.
var ErrInvalidToken = errors.New("invalid or expired token")

// Access issues a short-lived access token.
func (i *Issuer) Access(p Principal) (string, time.Time, error) {
	now := i.now()
	exp := now.Add(i.ttl)
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{
		Role: p.Role, CustomerID: p.CustomerID, Email: p.Email,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer,
			Subject:   strconv.FormatInt(p.UserID, 10),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	})
	s, err := tok.SignedString(i.secret)
	return s, exp, err
}

// Parse validates an access token.
func (i *Issuer) Parse(token string) (Principal, error) {
	var c claims
	_, err := jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) { return i.secret, nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer(issuer), jwt.WithTimeFunc(i.now), jwt.WithExpirationRequired())
	if err != nil {
		return Principal{}, ErrInvalidToken
	}
	id, err := strconv.ParseInt(c.Subject, 10, 64)
	if err != nil || !c.Role.Valid() {
		return Principal{}, ErrInvalidToken
	}
	return Principal{UserID: id, Role: c.Role, CustomerID: c.CustomerID, Email: c.Email}, nil
}

// NewRefreshToken returns an opaque random token and its SHA-256 (stored).
func NewRefreshToken() (token string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashRefreshToken(token), nil
}

// HashRefreshToken hashes a presented refresh token for lookup.
func HashRefreshToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
