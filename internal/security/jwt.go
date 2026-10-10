package security

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const jwtIssuer = "GoServer"

var ErrInvalidToken = errors.New("invalid token")

type JWTManager struct {
	secret []byte
	ttl    time.Duration
}

func NewJWTManager(secret string, ttl time.Duration) (*JWTManager, error) {
	if len(secret) < 32 {
		return nil, errors.New("JWT secret must be at least 32 bytes")
	}
	if ttl <= 0 {
		return nil, errors.New("JWT ttl must be positive")
	}

	return &JWTManager{
		secret: []byte(secret),
		ttl:    ttl,
	}, nil
}

// 为指定用户签发JWT
func (m *JWTManager) Generate(userID uint64) (string, error) {
	if userID == 0 {
		return "", errors.New("invalid user ID")
	}

	now := time.Now()

	claims := jwt.RegisteredClaims{
		Subject:   strconv.FormatUint(userID, 10),
		Issuer:    jwtIssuer,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(m.ttl)),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	return token.SignedString(m.secret)
}

// 验证JWT，成功返回userID
func (m *JWTManager) Verify(tokenString string) (uint64, error) {
	claims := &jwt.RegisteredClaims{}
	token, err := jwt.ParseWithClaims(
		tokenString,
		claims,
		func(token *jwt.Token) (any, error) {
			return m.secret, nil
		},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithIssuer(jwtIssuer),
		jwt.WithIssuedAt(),
	)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	if !token.Valid {
		return 0, ErrInvalidToken
	}

	userID, err := strconv.ParseUint(claims.Subject, 10, 64)
	if err != nil || userID == 0 {
		return 0, ErrInvalidToken
	}

	return userID, nil
}

func (m *JWTManager) ExpiresInSeconds() int64 {
	return int64(m.ttl / time.Second)
}
