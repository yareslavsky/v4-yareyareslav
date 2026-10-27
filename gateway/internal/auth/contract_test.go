package auth

import (
	"context"
	"encoding/json"
	"marketplace/gateway/pkg/jwt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi"
	"github.com/go-playground/validator"
	jwtlib "github.com/golang-jwt/jwt/v5"
)

const authTestID = "11111111-1111-4111-8111-111111111111"

type authRepository struct{ user User }

func (r *authRepository) CreateUser(_ context.Context, user User) (User, error) {
	id := UserID(authTestID)
	user.ID = &id
	r.user = user
	return user, nil
}
func (r *authRepository) FindUser(_ context.Context, email string) (User, error) {
	if email != r.user.Email {
		return User{}, ErrUserNotFound
	}
	return r.user, nil
}
func authRouter(repository Repository, manager *jwt.Manager) http.Handler {
	router := chi.NewRouter()
	NewAuthHandler(router, validator.New(), NewAuthService(repository, manager))
	return router
}
func authRequest(router http.Handler, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest("POST", path, strings.NewReader(body))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}
func assertAuthError(t *testing.T, recorder *httptest.ResponseRecorder, code int, name string) {
	t.Helper()
	if recorder.Code != code {
		t.Fatalf("status %d, body %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Content-Type") != "application/json" {
		t.Error("expected JSON")
	}
	var body ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error != name || body.Message == "" {
		t.Errorf("error body: %+v", body)
	}
}

func TestAuthTokenLifecycle(t *testing.T) {
	manager := jwt.NewManager("test-secret", jwt.AccessTTL, jwt.RefreshTTL)
	router := authRouter(&authRepository{}, manager)
	registered := authRequest(router, "/auth/register", `{"email":"buyer@example.com","password":"hunter22","role":"buyer"}`)
	if registered.Code != 201 {
		t.Fatalf("register: %d %s", registered.Code, registered.Body.String())
	}
	var registration RegisterUserResponse
	if err := json.Unmarshal(registered.Body.Bytes(), &registration); err != nil {
		t.Fatal(err)
	}
	if registration.User.ID != authTestID || registration.User.Role != "buyer" {
		t.Errorf("user: %+v", registration.User)
	}
	loggedIn := authRequest(router, "/auth/login", `{"email":"buyer@example.com","password":"hunter22"}`)
	if loggedIn.Code != 200 {
		t.Fatalf("login: %d %s", loggedIn.Code, loggedIn.Body.String())
	}
	var login LoginUserResponse
	if err := json.Unmarshal(loggedIn.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}
	for _, pair := range []LoginUserResponse{{AccessToken: registration.AccessToken, RefreshToken: registration.RefreshToken}, login} {
		if pair.AccessToken == pair.RefreshToken {
			t.Fatal("tokens in a pair must differ")
		}
		for _, test := range []struct {
			token, typ string
			ttl        time.Duration
		}{{pair.AccessToken, "access", 5 * time.Minute}, {pair.RefreshToken, "refresh", 7 * 24 * time.Hour}} {
			claims, err := manager.Parse(test.token)
			if err != nil {
				t.Fatal(err)
			}
			if claims.Subject != authTestID || claims.Role != "buyer" || claims.TokenType != test.typ || claims.ExpiresAt.Sub(claims.IssuedAt.Time) != test.ttl {
				t.Errorf("claims: %+v", claims)
			}
		}
	}
	body, err := json.Marshal(RefreshTokenRequest{RefreshToken: login.RefreshToken})
	if err != nil {
		t.Fatal(err)
	}
	refreshed := authRequest(router, "/auth/refresh", string(body))
	if refreshed.Code != 200 {
		t.Fatalf("refresh: %d %s", refreshed.Code, refreshed.Body.String())
	}
	var refresh RefreshTokenResponse
	if err := json.Unmarshal(refreshed.Body.Bytes(), &refresh); err != nil {
		t.Fatal(err)
	}
	accessClaims, err := manager.Parse(refresh.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	refreshClaims, err := manager.Parse(refresh.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if accessClaims.TokenType != "access" || refreshClaims.TokenType != "refresh" || refresh.AccessToken == refresh.RefreshToken || accessClaims.Subject != authTestID || refreshClaims.Role != "buyer" {
		t.Error("invalid refreshed pair")
	}
	assertAuthError(t, authRequest(router, "/auth/login", `{"email":"buyer@example.com","password":"wrong"}`), 401, "unauthenticated")
	assertAuthError(t, authRequest(router, "/auth/login", `{"email":"missing@example.com","password":"hunter22"}`), 401, "unauthenticated")
}

func TestRefreshRejectsInvalidTokens(t *testing.T) {
	manager := jwt.NewManager("test-secret", jwt.AccessTTL, jwt.RefreshTTL)
	router := authRouter(nil, manager)
	access, err := manager.GenerateAccessToken(authTestID, "buyer")
	if err != nil {
		t.Fatal(err)
	}
	sign := func(claims jwtlib.MapClaims, secret string) string {
		value, err := jwtlib.NewWithClaims(jwtlib.SigningMethodHS256, claims).SignedString([]byte(secret))
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	claims := func() jwtlib.MapClaims {
		return jwtlib.MapClaims{"sub": authTestID, "role": "buyer", "typ": "refresh", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
	}
	expired := claims()
	expired["exp"] = time.Now().Add(-time.Minute).Unix()
	noExpiration := claims()
	delete(noExpiration, "exp")
	noSubject := claims()
	delete(noSubject, "sub")
	noType := claims()
	delete(noType, "typ")
	noRole := claims()
	delete(noRole, "role")
	for _, test := range []struct{ name, token string }{
		{"access", access}, {"malformed", "bad"}, {"empty", ""},
		{"expired", sign(expired, "test-secret")}, {"wrong signature", sign(claims(), "wrong")},
		{"missing expiration", sign(noExpiration, "test-secret")},
		{"missing subject", sign(noSubject, "test-secret")},
		{"missing type", sign(noType, "test-secret")},
		{"missing role", sign(noRole, "test-secret")},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(RefreshTokenRequest{RefreshToken: test.token})
			if err != nil {
				t.Fatal(err)
			}
			result := authRequest(router, "/auth/refresh", string(body))
			assertAuthError(t, result, 401, "unauthenticated")
		})
	}
	assertAuthError(t, authRequest(router, "/auth/refresh", `{}`), 401, "unauthenticated")
}

func TestAuthBodyErrorsAreJSON(t *testing.T) {
	manager := jwt.NewManager("test-secret", jwt.AccessTTL, jwt.RefreshTTL)
	router := authRouter(nil, manager)
	for _, path := range []string{"/auth/register", "/auth/login", "/auth/refresh"} {
		for _, body := range []string{`{`, `null`, `{} {}`, `[]`, `{"refresh_token":1}`} {
			t.Run(path+body, func(t *testing.T) {
				result := authRequest(router, path, body)
				assertAuthError(t, result, 400, "validation_failed")
				var errorBody ErrorResponse
				if err := json.Unmarshal(result.Body.Bytes(), &errorBody); err != nil {
					t.Fatal(err)
				}
				if len(errorBody.Fields) == 0 {
					t.Error("validation fields missing")
				}
			})
		}
	}
	result := authRequest(router, "/auth/register", `{}`)
	assertAuthError(t, result, 400, "validation_failed")
	var body ErrorResponse
	if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	fields := map[string]bool{}
	for _, field := range body.Fields {
		fields[field.Field] = true
	}
	if len(fields) != 3 || !fields["email"] || !fields["password"] || !fields["role"] {
		t.Errorf("fields: %+v", body.Fields)
	}
}

func TestRegistrationPasswordByteLimit(t *testing.T) {
	manager := jwt.NewManager("test-secret", jwt.AccessTTL, jwt.RefreshTTL)
	for _, test := range []struct {
		name, password string
		code           int
	}{
		{"ASCII boundary", strings.Repeat("a", 72), 201},
		{"ASCII too long", strings.Repeat("a", 73), 400},
		{"UTF-8 boundary", strings.Repeat("\u044f", 36), 201},
		{"UTF-8 too long", strings.Repeat("\u044f", 37), 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &authRepository{}
			router := authRouter(repository, manager)
			data, err := json.Marshal(RegisterUserRequest{Email: "buyer@example.com", Password: test.password, Role: "buyer"})
			if err != nil {
				t.Fatal(err)
			}
			result := authRequest(router, "/auth/register", string(data))
			if result.Code != test.code {
				t.Fatalf("status %d, body %s", result.Code, result.Body.String())
			}
			if test.code == 400 {
				assertAuthError(t, result, 400, "validation_failed")
				var body ErrorResponse
				if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if len(body.Fields) != 1 || body.Fields[0].Field != "password" {
					t.Errorf("validation fields: %+v", body.Fields)
				}
				if repository.user.ID != nil {
					t.Error("invalid password reached repository")
				}
			}
		})
	}
}
