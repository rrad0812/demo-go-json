package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hashed, err := hashPassword("secret123")
	if err != nil {
		t.Fatalf("hashPassword failed: %v", err)
	}

	if err := verifyPassword(hashed, "secret123"); err != nil {
		t.Fatalf("verifyPassword failed for matching password: %v", err)
	}

	if err := verifyPassword(hashed, "wrong-password"); err == nil {
		t.Fatalf("expected verifyPassword to fail for mismatched password")
	}
}

func TestLoginCreatesSessionCookie(t *testing.T) {
	server, mock, cleanup := newAuthTestServer(t)
	defer cleanup()

	hashedPassword, err := hashPassword("admin-password")
	if err != nil {
		t.Fatalf("hashPassword failed: %v", err)
	}

	mock.ExpectQuery(`SELECT id, username, password_hash, is_admin, created_at FROM users WHERE username = \$1 AND is_deleted = FALSE`).
		WithArgs("admin").
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "password_hash", "is_admin", "created_at"}).
			AddRow(1, "admin", hashedPassword, true, time.Now()))

	reqBody := `{"username":"admin","password":"admin-password"}`
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var response map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if response["message"] != "login successful" {
		t.Fatalf("expected login successful message, got %v", response["message"])
	}

	cookie := rr.Result().Cookies()
	if len(cookie) == 0 {
		t.Fatalf("expected session cookie to be set")
	}

	var sessionCookie *http.Cookie
	var csrfCookie *http.Cookie
	for _, c := range cookie {
		if c.Name == "session" {
			sessionCookie = c
		}
		if c.Name == "csrf_token" {
			csrfCookie = c
		}
	}

	if sessionCookie == nil || sessionCookie.Value == "" {
		t.Fatalf("expected non-empty session cookie, got %+v", cookie)
	}
	if csrfCookie == nil || csrfCookie.Value == "" {
		t.Fatalf("expected non-empty csrf cookie, got %+v", cookie)
	}
	if response["csrf_token"] == "" {
		t.Fatalf("expected csrf_token in login response")
	}
	if response["csrf_token"] != csrfCookie.Value {
		t.Fatalf("expected csrf_token response to match csrf cookie")
	}
	if session := server.sessionStore.GetSession(sessionCookie.Value); session == nil {
		t.Fatalf("expected session to be stored")
	} else if session.UserID != 1 || session.Username != "admin" || !session.IsAdmin {
		t.Fatalf("unexpected session contents: %+v", session)
	}
}

func TestLoginRejectsInvalidPassword(t *testing.T) {
	server, mock, cleanup := newAuthTestServer(t)
	defer cleanup()

	hashedPassword, err := hashPassword("admin-password")
	if err != nil {
		t.Fatalf("hashPassword failed: %v", err)
	}

	mock.ExpectQuery(`SELECT id, username, password_hash, is_admin, created_at FROM users WHERE username = \$1 AND is_deleted = FALSE`).
		WithArgs("admin").
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "password_hash", "is_admin", "created_at"}).
			AddRow(1, "admin", hashedPassword, true, time.Now()))

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"admin","password":"wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestLoginRejectsInvalidJSON(t *testing.T) {
	server, _, cleanup := newAuthTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestLoginRejectsMissingCredentials(t *testing.T) {
	server, _, cleanup := newAuthTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestLoginRejectsUnknownUser(t *testing.T) {
	server, mock, cleanup := newAuthTestServer(t)
	defer cleanup()

	mock.ExpectQuery(`SELECT id, username, password_hash, is_admin, created_at FROM users WHERE username = \$1 AND is_deleted = FALSE`).
		WithArgs("missing").
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "password_hash", "is_admin", "created_at"}))

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"missing","password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestLoginSecureCookieRespectsTrustProxyHeaders(t *testing.T) {
	t.Run("does not trust forwarded proto by default", func(t *testing.T) {
		server, mock, cleanup := newAuthTestServerWithSecurity(t, SecurityConfig{})
		defer cleanup()

		hashedPassword, err := hashPassword("admin-password")
		if err != nil {
			t.Fatalf("hashPassword failed: %v", err)
		}

		mock.ExpectQuery(`SELECT id, username, password_hash, is_admin, created_at FROM users WHERE username = \$1 AND is_deleted = FALSE`).
			WithArgs("admin").
			WillReturnRows(sqlmock.NewRows([]string{"id", "username", "password_hash", "is_admin", "created_at"}).
				AddRow(1, "admin", hashedPassword, true, time.Now()))

		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"admin","password":"admin-password"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-Proto", "https")
		rr := httptest.NewRecorder()

		server.router.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
		}

		for _, c := range rr.Result().Cookies() {
			if c.Name == "session" && c.Secure {
				t.Fatalf("expected session cookie Secure=false when trust_proxy_headers is disabled")
			}
		}
	})

	t.Run("trusts forwarded proto when enabled", func(t *testing.T) {
		server, mock, cleanup := newAuthTestServerWithSecurity(t, SecurityConfig{TrustProxyHeaders: true})
		defer cleanup()

		hashedPassword, err := hashPassword("admin-password")
		if err != nil {
			t.Fatalf("hashPassword failed: %v", err)
		}

		mock.ExpectQuery(`SELECT id, username, password_hash, is_admin, created_at FROM users WHERE username = \$1 AND is_deleted = FALSE`).
			WithArgs("admin").
			WillReturnRows(sqlmock.NewRows([]string{"id", "username", "password_hash", "is_admin", "created_at"}).
				AddRow(1, "admin", hashedPassword, true, time.Now()))

		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"admin","password":"admin-password"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-Proto", "https")
		rr := httptest.NewRecorder()

		server.router.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
		}

		sawSecureSessionCookie := false
		for _, c := range rr.Result().Cookies() {
			if c.Name == "session" && c.Secure {
				sawSecureSessionCookie = true
			}
		}
		if !sawSecureSessionCookie {
			t.Fatalf("expected session cookie Secure=true when trust_proxy_headers is enabled")
		}
	})
}

func TestLogoutClearsSession(t *testing.T) {
	server, _, cleanup := newAuthTestServer(t)
	defer cleanup()

	token := server.sessionStore.CreateSession(&User{ID: 7, Username: "tester", IsAdmin: false})
	csrfToken := server.sessionStore.GetCSRFToken(token)

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	req.Header.Set("X-CSRF-Token", csrfToken)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	if server.sessionStore.GetSession(token) != nil {
		t.Fatalf("expected session to be removed")
	}

	cookies := rr.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatalf("expected logout cookie to be set")
	}

	var sessionDeleteCookie *http.Cookie
	var csrfDeleteCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "session" {
			sessionDeleteCookie = c
		}
		if c.Name == "csrf_token" {
			csrfDeleteCookie = c
		}
	}
	if sessionDeleteCookie == nil || sessionDeleteCookie.MaxAge != -1 {
		t.Fatalf("expected session cookie deletion, got %+v", cookies)
	}
	if csrfDeleteCookie == nil || csrfDeleteCookie.MaxAge != -1 {
		t.Fatalf("expected csrf cookie deletion, got %+v", cookies)
	}
}

func TestLogoutWithoutCookieStillSucceeds(t *testing.T) {
	server, _, cleanup := newAuthTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGetAuthSessionRequiresSession(t *testing.T) {
	server, _, cleanup := newAuthTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestHealthEndpointReturnsOK(t *testing.T) {
	server, _, cleanup := newAuthTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestReadyEndpointReturnsOKWhenDatabaseIsReachable(t *testing.T) {
	server, mock, cleanup := newAuthTestServer(t)
	defer cleanup()

	mock.ExpectQuery(`SELECT 1`).WillReturnRows(sqlmock.NewRows([]string{"?column?"}).AddRow(1))

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestReadyEndpointReturnsServiceUnavailableWhenDatabaseFails(t *testing.T) {
	server, mock, cleanup := newAuthTestServer(t)
	defer cleanup()

	mock.ExpectQuery(`SELECT 1`).WillReturnError(assertAnError())

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGetAuthSessionReturnsCurrentSessionAndCSRF(t *testing.T) {
	server, _, cleanup := newAuthTestServer(t)
	defer cleanup()

	token := server.sessionStore.CreateSession(&User{ID: 12, Username: "tester", IsAdmin: false})
	csrfToken := server.sessionStore.GetCSRFToken(token)

	req := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var response map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if response["authenticated"] != true {
		t.Fatalf("expected authenticated=true, got %v", response["authenticated"])
	}
	if response["csrf_token"] != csrfToken {
		t.Fatalf("expected csrf_token to match session token")
	}

	userRaw, ok := response["user"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected user object, got %T", response["user"])
	}
	if userRaw["username"] != "tester" {
		t.Fatalf("expected username tester, got %v", userRaw["username"])
	}
}

func TestRefreshCSRFTokenRequiresCSRFHeaderForBrowserRequest(t *testing.T) {
	server, _, cleanup := newAuthTestServer(t)
	defer cleanup()

	token := server.sessionStore.CreateSession(&User{ID: 12, Username: "tester", IsAdmin: false})

	req := httptest.NewRequest(http.MethodPost, "/auth/csrf/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	req.Header.Set("Origin", "http://localhost:3000")
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestRefreshCSRFTokenRotatesToken(t *testing.T) {
	server, _, cleanup := newAuthTestServer(t)
	defer cleanup()

	token := server.sessionStore.CreateSession(&User{ID: 12, Username: "tester", IsAdmin: false})
	oldCSRFToken := server.sessionStore.GetCSRFToken(token)

	req := httptest.NewRequest(http.MethodPost, "/auth/csrf/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("X-CSRF-Token", oldCSRFToken)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var response map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	newCSRFToken, ok := response["csrf_token"].(string)
	if !ok || newCSRFToken == "" {
		t.Fatalf("expected non-empty csrf_token in response")
	}
	if newCSRFToken == oldCSRFToken {
		t.Fatalf("expected rotated csrf token to differ from previous token")
	}

	storedCSRFToken := server.sessionStore.GetCSRFToken(token)
	if storedCSRFToken != newCSRFToken {
		t.Fatalf("expected rotated token in store to match response token")
	}

	cookies := rr.Result().Cookies()
	var csrfCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "csrf_token" {
			csrfCookie = c
			break
		}
	}
	if csrfCookie == nil || csrfCookie.Value != newCSRFToken {
		t.Fatalf("expected refreshed csrf cookie to match rotated token")
	}
}

func TestCSRFMiddlewareBlocksMutatingRequestWithoutToken(t *testing.T) {
	server, _, cleanup := newAuthTestServer(t)
	defer cleanup()

	token := server.sessionStore.CreateSession(&User{ID: 7, Username: "tester", IsAdmin: false})

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	req.Header.Set("Origin", "http://localhost:3000")
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestCSRFMiddlewareAllowsMutatingRequestWithToken(t *testing.T) {
	server, _, cleanup := newAuthTestServer(t)
	defer cleanup()

	token := server.sessionStore.CreateSession(&User{ID: 7, Username: "tester", IsAdmin: false})
	csrfToken := server.sessionStore.GetCSRFToken(token)

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("X-CSRF-Token", csrfToken)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSessionStoreExpiresSession(t *testing.T) {
	store := NewSessionStore(20 * time.Millisecond)
	token := store.CreateSession(&User{ID: 1, Username: "short", IsAdmin: false})

	if session := store.GetSession(token); session == nil {
		t.Fatalf("expected session to exist immediately")
	}

	time.Sleep(30 * time.Millisecond)

	if session := store.GetSession(token); session != nil {
		t.Fatalf("expected session to expire")
	}
}

func TestLoginRateLimitBlocksRepeatedFailures(t *testing.T) {
	server, mock, cleanup := newAuthTestServerWithSecurity(t, SecurityConfig{
		SessionTTLMinutes: 60,
		LoginRateLimit: LoginRateLimitConfig{
			MaxAttempts:   1,
			WindowSeconds: 60,
		},
	})
	defer cleanup()

	hashedPassword, err := hashPassword("correct")
	if err != nil {
		t.Fatalf("hashPassword failed: %v", err)
	}

	mock.ExpectQuery(`SELECT id, username, password_hash, is_admin, created_at FROM users WHERE username = \$1 AND is_deleted = FALSE`).
		WithArgs("admin").
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "password_hash", "is_admin", "created_at"}).
			AddRow(1, "admin", hashedPassword, true, time.Now()))

	body := `{"username":"admin","password":"wrong"}`
	req1 := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	req1.RemoteAddr = "10.0.0.1:1234"
	rr1 := httptest.NewRecorder()
	server.router.ServeHTTP(rr1, req1)

	if rr1.Code != http.StatusUnauthorized {
		t.Fatalf("expected first request status 401, got %d: %s", rr1.Code, rr1.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.RemoteAddr = "10.0.0.1:1234"
	rr2 := httptest.NewRecorder()
	server.router.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected second request status 429, got %d: %s", rr2.Code, rr2.Body.String())
	}
}

func TestCORSPreflightAllowedOrigin(t *testing.T) {
	server, _, cleanup := newAuthTestServerWithSecurity(t, SecurityConfig{
		AllowedOrigins: []string{"http://example.com"},
	})
	defer cleanup()

	req := httptest.NewRequest(http.MethodOptions, "/api/modules", nil)
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d: %s", rr.Code, rr.Body.String())
	}

	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "http://example.com" {
		t.Fatalf("expected Access-Control-Allow-Origin header, got %q", got)
	}
}

func TestErrorResponseContractShape(t *testing.T) {
	server, _, cleanup := newAuthTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/modules", nil)
	rr := httptest.NewRecorder()
	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d: %s", rr.Code, rr.Body.String())
	}

	var body map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}

	requiredFields := []string{"error", "code", "message", "details", "request_id"}
	for _, field := range requiredFields {
		if _, ok := body[field]; !ok {
			t.Fatalf("expected error response field %q to exist, got: %+v", field, body)
		}
	}

	requestIDHeader := rr.Header().Get("X-Request-ID")
	if requestIDHeader == "" {
		t.Fatalf("expected X-Request-ID header to be set")
	}

	requestIDBody, ok := body["request_id"].(string)
	if !ok || requestIDBody == "" {
		t.Fatalf("expected non-empty request_id in response body, got %v", body["request_id"])
	}

	if requestIDBody != requestIDHeader {
		t.Fatalf("expected request_id body/header match, got body=%q header=%q", requestIDBody, requestIDHeader)
	}
}

func newAuthTestServer(t *testing.T) (*APIServer, sqlmock.Sqlmock, func()) {
	return newAuthTestServerWithSecurity(t, SecurityConfig{})
}

func newAuthTestServerWithSecurity(t *testing.T, security SecurityConfig) (*APIServer, sqlmock.Sqlmock, func()) {
	t.Helper()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock db: %v", err)
	}

	appCfg := &AppConfig{
		Config:  Config{Security: security},
		Modules: map[string]*ModuleDefinition{},
	}
	server := NewAPIServer(appCfg, &SQLDataset{db: db, config: appCfg})

	cleanup := func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet sql expectations: %v", err)
		}
	}

	return server, mock, cleanup
}

func assertAnError() error {
	return &temporaryTestError{msg: "db down"}
}

type temporaryTestError struct {
	msg string
}

func (e *temporaryTestError) Error() string {
	if e == nil {
		return ""
	}
	return e.msg
}
