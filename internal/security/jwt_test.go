package security

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestJWT(t *testing.T) {
	// 测试专用密钥，不能用于生产环境
	secret := strings.Repeat("a", 32)

	manager, err := NewJWTManager(secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	// 首先生成一个合法的 Token
	tokenString, err := manager.Generate(81)
	if err != nil {
		t.Fatalf("生成 Token 失败: %v", err)
	}

	// 1. 正常签发和验证
	t.Run("正常Token", func(t *testing.T) {
		userID, err := manager.Verify(tokenString)
		if err != nil {
			t.Fatalf("验证失败: %v", err)
		}

		if userID != 81 {
			t.Fatalf("期望 userID=81，实际=%d", userID)
		}
	})

	// 2. 使用错误密钥验证
	t.Run("错误签名密钥", func(t *testing.T) {
		wrongManager, err := NewJWTManager(
			strings.Repeat("b", 32),
			time.Hour,
		)
		if err != nil {
			t.Fatal(err)
		}

		assertTokenRejected(t, wrongManager, tokenString)
	})

	// 3. 篡改 Payload 中的 sub
	t.Run("篡改Claims", func(t *testing.T) {
		parts := strings.Split(tokenString, ".")
		if len(parts) != 3 {
			t.Fatal("JWT 格式错误")
		}

		// 解码 Payload
		payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			t.Fatal(err)
		}

		var payload map[string]any
		if err := json.Unmarshal(payloadBytes, &payload); err != nil {
			t.Fatal(err)
		}

		// 攻击者尝试冒充 82 号用户
		payload["sub"] = "82"

		// 重新编码 Payload，但不重新签名
		modified, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}

		parts[1] = base64.RawURLEncoding.EncodeToString(modified)
		tamperedToken := strings.Join(parts, ".")

		assertTokenRejected(t, manager, tamperedToken)
	})

	// 4. 已经过期的 Token
	t.Run("过期Token", func(t *testing.T) {
		claims := jwt.RegisteredClaims{
			Subject:   "81",
			Issuer:    jwtIssuer,
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
		}

		expiredToken := signJWTForTest(
			t, jwt.SigningMethodHS256, claims, secret,
		)

		assertTokenRejected(t, manager, expiredToken)
	})

	// 5. 缺少 exp 字段
	t.Run("缺少exp", func(t *testing.T) {
		claims := jwt.RegisteredClaims{
			Subject:  "81",
			Issuer:   jwtIssuer,
			IssuedAt: jwt.NewNumericDate(time.Now()),
		}

		noExpToken := signJWTForTest(
			t, jwt.SigningMethodHS256, claims, secret,
		)

		assertTokenRejected(t, manager, noExpToken)
	})

	// 6. 使用非预期的签名算法
	t.Run("错误签名算法", func(t *testing.T) {
		claims := jwt.RegisteredClaims{
			Subject:   "81",
			Issuer:    jwtIssuer,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		}

		wrongAlgToken := signJWTForTest(
			t, jwt.SigningMethodHS384, claims, secret,
		)

		assertTokenRejected(t, manager, wrongAlgToken)
	})

	// 7. Subject 不是合法的用户 ID
	t.Run("非法用户ID", func(t *testing.T) {
		claims := jwt.RegisteredClaims{
			Subject:   "not-a-number",
			Issuer:    jwtIssuer,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		}

		invalidSubToken := signJWTForTest(
			t, jwt.SigningMethodHS256, claims, secret,
		)

		assertTokenRejected(t, manager, invalidSubToken)
	})

	// 8. 签发者不正确
	t.Run("错误Issuer", func(t *testing.T) {
		claims := jwt.RegisteredClaims{
			Subject:   "81",
			Issuer:    "OtherService",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		}

		wrongIssuerToken := signJWTForTest(
			t, jwt.SigningMethodHS256, claims, secret,
		)

		assertTokenRejected(t, manager, wrongIssuerToken)
	})

	// 9. 空 Token
	t.Run("空Token", func(t *testing.T) {
		assertTokenRejected(t, manager, "")
	})

	// 10. 不允许为 ID=0 的用户签发 Token
	t.Run("拒绝签发非法用户ID", func(t *testing.T) {
		_, err := manager.Generate(0)
		if err == nil {
			t.Fatal("userID=0 应该被拒绝")
		}
	})
}

// 辅助函数：手动签发指定 Claims 的 Token
// 用于构造过期、错误算法等负例
func signJWTForTest(
	t *testing.T,
	method jwt.SigningMethod,
	claims jwt.RegisteredClaims,
	secret string,
) string {
	t.Helper()

	token := jwt.NewWithClaims(method, claims)

	tokenString, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("签发测试 Token 失败: %v", err)
	}

	return tokenString
}

// 辅助函数：断言 Token 必须被拒绝
func assertTokenRejected(
	t *testing.T,
	manager *JWTManager,
	tokenString string,
) {
	t.Helper()

	_, err := manager.Verify(tokenString)

	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("期望 ErrInvalidToken，实际得到: %v", err)
	}
}
