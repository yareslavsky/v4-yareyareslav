package jwt

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"
)

const testUserID = "11111111-1111-4111-8111-111111111111"

func validClaims() jwtlib.MapClaims {
	now := time.Now()
	return jwtlib.MapClaims{"sub": testUserID, "role": "buyer", "typ": "access", "iat": now.Unix(), "exp": now.Add(AccessTTL).Unix()}
}
func signedToken(t *testing.T, method jwtlib.SigningMethod, claims jwtlib.MapClaims, secret string) string {
	t.Helper()
	token, err := jwtlib.NewWithClaims(method, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestTokenContract(t *testing.T) {
	manager := NewManager("test-secret", AccessTTL, RefreshTTL)
	access, err := manager.GenerateAccessToken(testUserID, "seller")
	if err != nil {
		t.Fatal(err)
	}
	refresh, err := manager.GenerateRefreshToken(testUserID, "seller")
	if err != nil {
		t.Fatal(err)
	}
	if access == refresh {
		t.Fatal("access and refresh must differ")
	}
	for _, test := range []struct {
		token, typ string
		ttl        time.Duration
	}{{access, "access", 5 * time.Minute}, {refresh, "refresh", 7 * 24 * time.Hour}} {
		t.Run(test.typ, func(t *testing.T) {
			parsed, err := jwtlib.Parse(test.token, func(token *jwtlib.Token) (any, error) { return []byte("test-secret"), nil }, jwtlib.WithValidMethods([]string{"HS256"}))
			if err != nil {
				t.Fatal(err)
			}
			claims := parsed.Claims.(jwtlib.MapClaims)
			for _, name := range []string{"sub", "role", "exp", "iat", "typ"} {
				if _, ok := claims[name]; !ok {
					t.Errorf("missing %s", name)
				}
			}
			if claims["sub"] != testUserID || claims["role"] != "seller" || claims["typ"] != test.typ {
				t.Errorf("claims: %v", claims)
			}
			if _, ok := claims["user_role"]; ok {
				t.Error("legacy role claim")
			}
			if _, ok := claims["token_type"]; ok {
				t.Error("legacy type claim")
			}
			issued, _ := claims.GetIssuedAt()
			expires, _ := claims.GetExpirationTime()
			if expires.Sub(issued.Time) != test.ttl {
				t.Errorf("TTL: %s", expires.Sub(issued.Time))
			}
			if _, err := manager.Parse(test.token); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestParseRejectsInvalidTokens(t *testing.T) {
	manager := NewManager("test-secret", AccessTTL, RefreshTTL)
	for _, test := range []struct {
		name   string
		change func(jwtlib.MapClaims)
	}{
		{"expired", func(c jwtlib.MapClaims) { c["exp"] = time.Now().Add(-time.Minute).Unix() }},
		{"missing expiration", func(c jwtlib.MapClaims) { delete(c, "exp") }},
		{"missing issued at", func(c jwtlib.MapClaims) { delete(c, "iat") }},
		{"future issued at", func(c jwtlib.MapClaims) { c["iat"] = time.Now().Add(time.Hour).Unix() }},
		{"missing subject", func(c jwtlib.MapClaims) { delete(c, "sub") }},
		{"invalid subject", func(c jwtlib.MapClaims) { c["sub"] = "bad" }},
		{"missing role", func(c jwtlib.MapClaims) { delete(c, "role") }},
		{"invalid role", func(c jwtlib.MapClaims) { c["role"] = "admin" }},
		{"missing type", func(c jwtlib.MapClaims) { delete(c, "typ") }},
		{"invalid type", func(c jwtlib.MapClaims) { c["typ"] = "other" }},
		{"legacy claims", func(c jwtlib.MapClaims) {
			c["user_role"] = c["role"]
			c["token_type"] = c["typ"]
			delete(c, "role")
			delete(c, "typ")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			claims := validClaims()
			test.change(claims)
			token := signedToken(t, jwtlib.SigningMethodHS256, claims, "test-secret")
			if _, err := manager.Parse(token); err == nil {
				t.Error("invalid token accepted")
			}
		})
	}
	for _, test := range []struct{ name, token string }{
		{"wrong secret", signedToken(t, jwtlib.SigningMethodHS256, validClaims(), "other-secret")},
		{"wrong algorithm", signedToken(t, jwtlib.SigningMethodHS384, validClaims(), "test-secret")},
		{"malformed", "not-a-token"},
		{"unsigned", "eyJhbGciOiJub25lIn0.eyJzdWIiOiIxIn0."},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := manager.Parse(test.token); err == nil {
				t.Error("invalid token accepted")
			}
		})
	}
}

func TestMiddlewareContract(t *testing.T) {
	manager := NewManager("test-secret", AccessTTL, RefreshTTL)
	middleware := NewJWTMiddleware(manager)
	buyer, err := manager.GenerateAccessToken(testUserID, "buyer")
	if err != nil {
		t.Fatal(err)
	}
	seller, err := manager.GenerateAccessToken(testUserID, "seller")
	if err != nil {
		t.Fatal(err)
	}
	refresh, err := manager.GenerateRefreshToken(testUserID, "buyer")
	if err != nil {
		t.Fatal(err)
	}
	expired := validClaims()
	expired["exp"] = time.Now().Add(-time.Minute).Unix()
	for _, test := range []struct {
		name, header string
		code         int
		errorName    string
	}{
		{"valid", "Bearer " + buyer, 204, ""},
		{"missing", "", 401, "unauthenticated"},
		{"bad header", "Basic " + buyer, 401, "unauthenticated"},
		{"malformed", "Bearer bad", 401, "unauthenticated"},
		{"refresh", "Bearer " + refresh, 401, "unauthenticated"},
		{"wrong role", "Bearer " + seller, 403, "forbidden"},
		{"expired", "Bearer " + signedToken(t, jwtlib.SigningMethodHS256, expired, "test-secret"), 401, "unauthenticated"},
		{"wrong signature", "Bearer " + signedToken(t, jwtlib.SigningMethodHS256, validClaims(), "other"), 401, "unauthenticated"},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			handler := middleware.CheckUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if UserID(r.Context()) != testUserID || Role(r.Context()) != "buyer" {
					t.Error("identity missing from context")
				}
				w.WriteHeader(http.StatusNoContent)
			}), "buyer")
			request := httptest.NewRequest("POST", "/", strings.NewReader("invalid JSON"))
			request.Header.Set("Authorization", test.header)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.code || called != (test.code == 204) {
				t.Fatalf("status %d, called %v, body %s", recorder.Code, called, recorder.Body.String())
			}
			if test.errorName != "" {
				var body map[string]any
				if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body["error"] != test.errorName || body["message"] == "" {
					t.Errorf("error body: %v", body)
				}
				if _, ok := body["fields"]; ok {
					t.Error("auth error must not include fields")
				}
			}
		})
	}
}
