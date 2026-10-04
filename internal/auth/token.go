// Package auth issues and verifies the proxy's stateless access tokens and
// enforces them on the inference and control API routes.
//
// Tokens are HS256 JWTs signed with a single server-held key. They are issued
// only by the replay-proxy binary (`replay-proxy token issue`); no HTTP route
// can mint one. Rotating the key invalidates every outstanding token.
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// Issuer is the required `iss` claim.
	Issuer = "llm-replay-proxy"
	// DefaultTTL is the default token lifetime for `token issue` (90 days).
	DefaultTTL = 90 * 24 * time.Hour
	// DefaultSubject is the `sub` claim when none is given.
	DefaultSubject = "local"
	// Leeway tolerates small clock differences for exp, nbf and iat.
	Leeway = 30 * time.Second
	// maxTokenBytes bounds work spent on obviously invalid credentials.
	maxTokenBytes = 4096
)

// ErrInvalidToken is returned for every verification failure. Callers should
// not reveal finer detail to clients.
var ErrInvalidToken = errors.New("invalid or expired access token")

// Claims are the verified contents of a token.
type Claims struct {
	Subject   string
	ID        string
	IssuedAt  time.Time
	ExpiresAt time.Time // zero when the token never expires
}

// Authority signs and verifies tokens with one HMAC key.
type Authority struct {
	key []byte
	now func() time.Time
}

// New returns an Authority for key.
func New(key Key) (*Authority, error) {
	if len(key.b) < MinKeyBytes {
		return nil, fmt.Errorf("signing key must be at least %d bytes", MinKeyBytes)
	}
	return &Authority{key: append([]byte(nil), key.b...), now: time.Now}, nil
}

// Issue signs a token for subject. A ttl of zero issues a token without an
// `exp` claim; such a token stays valid until the signing key is rotated.
func (a *Authority) Issue(subject string, ttl time.Duration) (string, Claims, error) {
	if ttl < 0 {
		return "", Claims{}, errors.New("ttl must not be negative")
	}
	subject = strings.TrimSpace(subject)
	if subject == "" {
		subject = DefaultSubject
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", Claims{}, err
	}
	now := a.now().Truncate(time.Second)
	rc := jwt.RegisteredClaims{
		Issuer:    Issuer,
		Subject:   subject,
		ID:        hex.EncodeToString(id[:]),
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
	}
	c := Claims{Subject: subject, ID: rc.ID, IssuedAt: now}
	if ttl > 0 {
		exp := now.Add(ttl)
		rc.ExpiresAt = jwt.NewNumericDate(exp)
		c.ExpiresAt = exp
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, rc).SignedString(a.key)
	if err != nil {
		return "", Claims{}, err
	}
	return token, c, nil
}

// Verify checks a token's signature and claims. Only HS256 is accepted (so
// `alg: none` and asymmetric-key confusion are rejected), the signature is
// compared in constant time, `iss` must equal Issuer, `iat` is required, and
// `exp`/`nbf`/`iat` are checked with Leeway. A token without `exp` is
// accepted because only this binary can sign tokens and it omits `exp` only
// when explicitly issued with `-ttl 0`.
func (a *Authority) Verify(token string) (Claims, error) {
	if token == "" || len(token) > maxTokenBytes {
		return Claims{}, ErrInvalidToken
	}
	var rc jwt.RegisteredClaims
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(Issuer),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(Leeway),
		jwt.WithTimeFunc(a.now),
		jwt.WithStrictDecoding(),
	)
	parsed, err := parser.ParseWithClaims(token, &rc, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, ErrInvalidToken
		}
		return a.key, nil
	})
	if err != nil || !parsed.Valid || rc.IssuedAt == nil {
		return Claims{}, ErrInvalidToken
	}
	c := Claims{Subject: rc.Subject, ID: rc.ID, IssuedAt: rc.IssuedAt.Time}
	if rc.ExpiresAt != nil {
		c.ExpiresAt = rc.ExpiresAt.Time
	}
	return c, nil
}
