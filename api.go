// api.go
package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv" // Potrebno za strconv.Atoi
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/mux"
)

const (
	defaultSessionTTLMinutes  = 60 * 24
	defaultLoginMaxAttempts   = 5
	defaultLoginWindowSeconds = 300
	requestIDHeader           = "X-Request-ID"
)

type contextKey string

const requestIDContextKey contextKey = "request_id"

// SessionStore čuva aktivne sesije: token -> UserSession
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*sessionEntry
	ttl      time.Duration
}

type sessionEntry struct {
	session   *UserSession
	csrfToken string
	expiresAt time.Time
}

type loginAttempt struct {
	count       int
	windowStart time.Time
}

type LoginRateLimiter struct {
	mu          sync.Mutex
	attempts    map[string]loginAttempt
	maxAttempts int
	window      time.Duration
}

// NewSessionStore kreira novi session store
func NewSessionStore(ttl time.Duration) *SessionStore {
	if ttl <= 0 {
		ttl = time.Duration(defaultSessionTTLMinutes) * time.Minute
	}

	return &SessionStore{
		sessions: make(map[string]*sessionEntry),
		ttl:      ttl,
	}
}

func NewLoginRateLimiter(maxAttempts int, window time.Duration) *LoginRateLimiter {
	if maxAttempts <= 0 {
		maxAttempts = defaultLoginMaxAttempts
	}
	if window <= 0 {
		window = time.Duration(defaultLoginWindowSeconds) * time.Second
	}

	return &LoginRateLimiter{
		attempts:    make(map[string]loginAttempt),
		maxAttempts: maxAttempts,
		window:      window,
	}
}

// CreateSession pravi novu sesiju i vraća token
func (ss *SessionStore) CreateSession(user *User) string {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	token, err := generateSessionToken()
	if err != nil {
		log.Printf("WARNING: failed to generate cryptographically random session token: %v", err)
		token = fmt.Sprintf("%d-%d", user.ID, time.Now().UnixNano())
	}
	csrfToken, err := generateSessionToken()
	if err != nil {
		log.Printf("WARNING: failed to generate cryptographically random CSRF token: %v", err)
		csrfToken = fmt.Sprintf("csrf-%d-%d", user.ID, time.Now().UnixNano())
	}
	ss.sessions[token] = &sessionEntry{
		session: &UserSession{
			UserID:   user.ID,
			Username: user.Username,
			IsAdmin:  user.IsAdmin,
		},
		csrfToken: csrfToken,
		expiresAt: time.Now().Add(ss.ttl),
	}
	return token
}

// GetSession pronalazi sesiju po tokenu
func (ss *SessionStore) GetSession(token string) *UserSession {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	entry, ok := ss.sessions[token]
	if !ok {
		return nil
	}

	if time.Now().After(entry.expiresAt) {
		delete(ss.sessions, token)
		return nil
	}

	return entry.session
}

func (ss *SessionStore) GetCSRFToken(token string) string {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	entry, ok := ss.sessions[token]
	if !ok {
		return ""
	}

	if time.Now().After(entry.expiresAt) {
		delete(ss.sessions, token)
		return ""
	}

	return entry.csrfToken
}

func (ss *SessionStore) RotateCSRFToken(token string) (string, bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	entry, ok := ss.sessions[token]
	if !ok {
		return "", false
	}

	if time.Now().After(entry.expiresAt) {
		delete(ss.sessions, token)
		return "", false
	}

	newToken, err := generateSessionToken()
	if err != nil {
		log.Printf("WARNING: failed to rotate CSRF token with crypto random: %v", err)
		newToken = fmt.Sprintf("csrf-%d", time.Now().UnixNano())
	}

	entry.csrfToken = newToken
	return newToken, true
}

// DeleteSession briše sesiju
func (ss *SessionStore) DeleteSession(token string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	delete(ss.sessions, token)
}

func generateSessionToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (rl *LoginRateLimiter) IsLimited(key string) (bool, time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	state, exists := rl.attempts[key]
	if !exists {
		return false, 0
	}

	now := time.Now()
	if now.Sub(state.windowStart) >= rl.window {
		delete(rl.attempts, key)
		return false, 0
	}

	if state.count >= rl.maxAttempts {
		remaining := rl.window - now.Sub(state.windowStart)
		if remaining < 0 {
			remaining = 0
		}
		return true, remaining
	}

	return false, 0
}

func (rl *LoginRateLimiter) RegisterFailure(key string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	state, exists := rl.attempts[key]
	if !exists || now.Sub(state.windowStart) >= rl.window {
		rl.attempts[key] = loginAttempt{count: 1, windowStart: now}
		return
	}

	state.count++
	rl.attempts[key] = state
}

func (rl *LoginRateLimiter) Reset(key string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.attempts, key)
}

// APIServer sadrži zavisnosti za API handlere.
type APIServer struct {
	config            *AppConfig
	dataset           *SQLDataset
	router            *mux.Router
	sessionStore      *SessionStore
	loginLimiter      *LoginRateLimiter
	allowedOrigins    map[string]struct{}
	allowAnyOrigin    bool
	trustProxyHeaders bool
	migrationsReady   bool
}

// NewAPIServer kreira novu instancu APIServer-a.
func NewAPIServer(config *AppConfig, dataset *SQLDataset) *APIServer {
	secCfg := deriveSecurityConfig(config)

	originMap := make(map[string]struct{})
	allowAny := false
	for _, origin := range secCfg.AllowedOrigins {
		o := strings.TrimSpace(origin)
		if o == "" {
			continue
		}
		if o == "*" {
			allowAny = true
			continue
		}
		originMap[o] = struct{}{}
	}

	s := &APIServer{
		config:            config,
		dataset:           dataset,
		router:            mux.NewRouter(),
		sessionStore:      NewSessionStore(time.Duration(secCfg.SessionTTLMinutes) * time.Minute),
		loginLimiter:      NewLoginRateLimiter(secCfg.LoginRateLimit.MaxAttempts, time.Duration(secCfg.LoginRateLimit.WindowSeconds)*time.Second),
		allowedOrigins:    originMap,
		allowAnyOrigin:    allowAny,
		trustProxyHeaders: secCfg.TrustProxyHeaders,
		migrationsReady:   true,
	}
	s.InitRoutes() // Inicijalizuj rute odmah po kreiranju servera
	return s
}

func deriveSecurityConfig(appCfg *AppConfig) SecurityConfig {
	security := SecurityConfig{
		AllowedOrigins:    []string{"http://localhost:3000", "http://localhost:8080"},
		SessionTTLMinutes: defaultSessionTTLMinutes,
		LoginRateLimit: LoginRateLimitConfig{
			MaxAttempts:   defaultLoginMaxAttempts,
			WindowSeconds: defaultLoginWindowSeconds,
		},
	}

	if appCfg == nil {
		return security
	}

	if len(appCfg.Config.Security.AllowedOrigins) > 0 {
		security.AllowedOrigins = appCfg.Config.Security.AllowedOrigins
	}
	if appCfg.Config.Security.SessionTTLMinutes > 0 {
		security.SessionTTLMinutes = appCfg.Config.Security.SessionTTLMinutes
	}
	if appCfg.Config.Security.LoginRateLimit.MaxAttempts > 0 {
		security.LoginRateLimit.MaxAttempts = appCfg.Config.Security.LoginRateLimit.MaxAttempts
	}
	if appCfg.Config.Security.LoginRateLimit.WindowSeconds > 0 {
		security.LoginRateLimit.WindowSeconds = appCfg.Config.Security.LoginRateLimit.WindowSeconds
	}
	if appCfg.Config.Security.TrustProxyHeaders {
		security.TrustProxyHeaders = true
	}

	return security
}

// InitRoutes inicijalizuje sve API rute.
func (s *APIServer) InitRoutes() {
	s.router.Use(s.requestIDMiddleware)
	s.router.Use(s.accessLogMiddleware)
	s.router.Use(s.corsMiddleware)
	s.router.Use(s.csrfMiddleware)
	s.router.PathPrefix("/").Methods(http.MethodOptions).HandlerFunc(s.HandlePreflight)

	// Health checks (accessible without auth)
	s.router.HandleFunc("/health", s.Health).Methods("GET")
	s.router.HandleFunc("/healthy", s.Health).Methods("GET")
	s.router.HandleFunc("/ready", s.Ready).Methods("GET")

	// Auth and user management
	s.router.HandleFunc("/login", s.Login).Methods("POST")
	s.router.HandleFunc("/logout", s.Logout).Methods("POST")
	s.router.HandleFunc("/auth/session", s.GetAuthSession).Methods("GET")
	s.router.HandleFunc("/auth/csrf/refresh", s.RefreshCSRFToken).Methods("POST")
	s.router.HandleFunc("/api/login", s.Login).Methods("POST")
	s.router.HandleFunc("/api/logout", s.Logout).Methods("POST")
	s.router.HandleFunc("/api/auth/session", s.GetAuthSession).Methods("GET")
	s.router.HandleFunc("/api/auth/csrf/refresh", s.RefreshCSRFToken).Methods("POST")
	s.router.HandleFunc("/api/users", s.CreateUser).Methods("POST")

	// Audit
	s.router.HandleFunc("/api/audit", s.GetAuditLogs).Methods("GET")

	// Module management
	s.router.HandleFunc("/api/modules", s.GetAllModules).Methods("GET")

	// Records: GET all, POST create
	s.router.HandleFunc("/api/{moduleID}", s.GetModuleRecords).Methods("GET")
	s.router.HandleFunc("/api/{moduleID}", s.CreateRecord).Methods("POST")

	// Single record: GET, PUT, DELETE
	s.router.HandleFunc("/api/{moduleID}/{recordID}", s.GetSingleRecord).Methods("GET")
	s.router.HandleFunc("/api/{moduleID}/{recordID}", s.UpdateRecord).Methods("PUT")
	s.router.HandleFunc("/api/{moduleID}/{recordID}", s.DeleteRecord).Methods("DELETE")

	// Submodule records: GET all, POST create
	s.router.HandleFunc("/api/{moduleID}/{recordID}/{submoduleID}", s.GetSubmoduleRecords).Methods("GET")
	s.router.HandleFunc("/api/{moduleID}/{recordID}/{submoduleID}", s.CreateSubmoduleRecord).Methods("POST")

	// Single submodule record: GET, PUT, DELETE
	s.router.HandleFunc("/api/{moduleID}/{recordID}/{submoduleID}/{childRecordID}", s.GetSubmoduleRecord).Methods("GET")
	s.router.HandleFunc("/api/{moduleID}/{recordID}/{submoduleID}/{childRecordID}", s.UpdateSubmoduleRecord).Methods("PUT")
	s.router.HandleFunc("/api/{moduleID}/{recordID}/{submoduleID}/{childRecordID}", s.DeleteSubmoduleRecord).Methods("DELETE")
}

func (s *APIServer) HandlePreflight(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

type statusCapturingResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusCapturingResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusCapturingResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

func (s *APIServer) requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requestID := strings.TrimSpace(req.Header.Get(requestIDHeader))
		if requestID == "" {
			requestID = nextRequestID()
		}

		ctx := context.WithValue(req.Context(), requestIDContextKey, requestID)
		req = req.WithContext(ctx)
		w.Header().Set(requestIDHeader, requestID)

		next.ServeHTTP(w, req)
	})
}

func (s *APIServer) accessLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		startedAt := time.Now()
		wrappedWriter := &statusCapturingResponseWriter{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(wrappedWriter, req)

		route := req.URL.Path
		if currentRoute := mux.CurrentRoute(req); currentRoute != nil {
			if routeTemplate, err := currentRoute.GetPathTemplate(); err == nil && strings.TrimSpace(routeTemplate) != "" {
				route = routeTemplate
			}
		}

		userID := "anonymous"
		if session := s.GetSession(req); session != nil {
			userID = fmt.Sprint(session.UserID)
		}

		log.Printf(
			"INFO: request_id=%s user_id=%s method=%s route=%s status=%d bytes=%d duration_ms=%d",
			requestIDFromContext(req),
			userID,
			req.Method,
			route,
			wrappedWriter.status,
			wrappedWriter.bytes,
			time.Since(startedAt).Milliseconds(),
		)
	})
}

func (s *APIServer) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		origin := strings.TrimSpace(req.Header.Get("Origin"))
		originAllowed := origin != "" && (s.allowAnyOrigin || s.isAllowedOrigin(origin))

		if originAllowed {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}

		if req.Method == http.MethodOptions {
			if origin != "" && !originAllowed {
				writeJSONError(w, http.StatusForbidden, "forbidden origin")
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, req)
	})
}

func (s *APIServer) isAllowedOrigin(origin string) bool {
	if s == nil {
		return false
	}
	_, ok := s.allowedOrigins[origin]
	return ok
}

func (s *APIServer) csrfMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !requiresCSRFProtection(req.Method) || req.URL.Path == "/login" || req.URL.Path == "/api/login" {
			next.ServeHTTP(w, req)
			return
		}

		sessionCookie, err := req.Cookie("session")
		if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
			next.ServeHTTP(w, req)
			return
		}

		expectedCSRF := s.sessionStore.GetCSRFToken(sessionCookie.Value)
		if expectedCSRF == "" {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		providedCSRF := strings.TrimSpace(req.Header.Get("X-CSRF-Token"))
		if providedCSRF == "" {
			providedCSRF = strings.TrimSpace(req.Header.Get("X-XSRF-Token"))
		}
		if providedCSRF == "" {
			writeJSONError(w, http.StatusForbidden, "forbidden: missing csrf token")
			return
		}

		if subtle.ConstantTimeCompare([]byte(providedCSRF), []byte(expectedCSRF)) != 1 {
			writeJSONError(w, http.StatusForbidden, "forbidden: invalid csrf token")
			return
		}

		next.ServeHTTP(w, req)
	})
}

func requiresCSRFProtection(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// Start pokreće HTTP server.
func (s *APIServer) Start(addr string) error {
	srv := &http.Server{
		Addr:         addr,
		Handler:      s.router, // Koristimo serverov router kao handler
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	log.Printf("INFO: API server pokrenut na adresi %s", addr)
	return srv.ListenAndServe()
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type moduleNode struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	Type     ModuleType   `json:"type"`
	Children []moduleNode `json:"children,omitempty"`
	Icon     string       `json:"icon,omitempty"`
}

func (s *APIServer) Login(w http.ResponseWriter, req *http.Request) {
	credentials, err := decodeLoginRequest(req)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	rateKey := buildLoginRateLimitKey(req, credentials.Username)
	if limited, retryAfter := s.loginLimiter.IsLimited(rateKey); limited {
		if retryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
		}
		writeJSONError(w, http.StatusTooManyRequests, "too many login attempts, try again later")
		return
	}

	if credentials.Username == "" || credentials.Password == "" {
		writeJSONError(w, http.StatusBadRequest, "username and password are required")
		return
	}

	user, err := s.dataset.GetUserByUsername(credentials.Username)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "server error")
		log.Printf("ERROR: GetUserByUsername failed: %v", err)
		return
	}
	if user == nil {
		s.loginLimiter.RegisterFailure(rateKey)
		writeJSONError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	if err := verifyPassword(user.PasswordHash, credentials.Password); err != nil {
		s.loginLimiter.RegisterFailure(rateKey)
		writeJSONError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	s.loginLimiter.Reset(rateKey)

	token := s.sessionStore.CreateSession(user)
	csrfToken := s.sessionStore.GetCSRFToken(token)
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    token,
		Path:     "/",
		MaxAge:   s.sessionMaxAgeSeconds(),
		HttpOnly: true,
		Secure:   isHTTPSRequest(req, s.trustProxyHeaders),
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     "csrf_token",
		Value:    csrfToken,
		Path:     "/",
		MaxAge:   s.sessionMaxAgeSeconds(),
		HttpOnly: false,
		Secure:   isHTTPSRequest(req, s.trustProxyHeaders),
		SameSite: http.SameSiteLaxMode,
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":    "login successful",
		"csrf_token": csrfToken,
		"user": map[string]interface{}{
			"id":       user.ID,
			"username": user.Username,
			"is_admin": user.IsAdmin,
		},
	})
}

func buildLoginRateLimitKey(req *http.Request, username string) string {
	keyUser := strings.ToLower(strings.TrimSpace(username))
	if keyUser == "" {
		keyUser = "anonymous"
	}
	return keyUser + "|" + clientIP(req)
}

func clientIP(req *http.Request) string {
	if req == nil {
		return "unknown"
	}

	if xff := strings.TrimSpace(req.Header.Get("X-Forwarded-For")); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}

	if xrip := strings.TrimSpace(req.Header.Get("X-Real-IP")); xrip != "" {
		return xrip
	}

	host, _, err := net.SplitHostPort(strings.TrimSpace(req.RemoteAddr))
	if err == nil && host != "" {
		return host
	}

	if strings.TrimSpace(req.RemoteAddr) != "" {
		return strings.TrimSpace(req.RemoteAddr)
	}

	return "unknown"
}

func (s *APIServer) Logout(w http.ResponseWriter, req *http.Request) {
	cookie, err := req.Cookie("session")
	if err == nil {
		s.sessionStore.DeleteSession(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isHTTPSRequest(req, s.trustProxyHeaders),
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     "csrf_token",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: false,
		Secure:   isHTTPSRequest(req, s.trustProxyHeaders),
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]string{"message": "logout successful"})
}

func (s *APIServer) Health(w http.ResponseWriter, _ *http.Request) {
	if err := writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "ok",
		"service": "demo-api",
	}); err != nil {
		log.Printf("ERROR: Greška pri enkodiranju Health odgovora: %v", err)
	}
}

func (s *APIServer) Ready(w http.ResponseWriter, _ *http.Request) {
	if s == nil || s.dataset == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "not ready: dataset unavailable")
		return
	}

	if !s.migrationsReady {
		writeJSONError(w, http.StatusServiceUnavailable, "not ready: migrations not finished")
		return
	}

	if err := s.dataset.ReadyCheck(); err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, fmt.Sprintf("not ready: database unavailable: %v", err))
		return
	}

	if err := writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":           "ready",
		"database":         "ok",
		"migrations_ready": s.migrationsReady,
	}); err != nil {
		log.Printf("ERROR: Greška pri enkodiranju Ready odgovora: %v", err)
	}
}

func (s *APIServer) GetAuthSession(w http.ResponseWriter, req *http.Request) {
	session, ok := s.requireSession(w, req)
	if !ok {
		return
	}

	sessionCookie, err := req.Cookie("session")
	if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	csrfToken := s.sessionStore.GetCSRFToken(sessionCookie.Value)
	if strings.TrimSpace(csrfToken) == "" {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"authenticated": true,
		"csrf_token":    csrfToken,
		"user": map[string]interface{}{
			"id":       session.UserID,
			"username": session.Username,
			"is_admin": session.IsAdmin,
		},
	})
}

func (s *APIServer) RefreshCSRFToken(w http.ResponseWriter, req *http.Request) {
	_, ok := s.requireSession(w, req)
	if !ok {
		return
	}

	sessionCookie, err := req.Cookie("session")
	if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	newCSRFToken, rotated := s.sessionStore.RotateCSRFToken(sessionCookie.Value)
	if !rotated || strings.TrimSpace(newCSRFToken) == "" {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "csrf_token",
		Value:    newCSRFToken,
		Path:     "/",
		MaxAge:   s.sessionMaxAgeSeconds(),
		HttpOnly: false,
		Secure:   isHTTPSRequest(req, s.trustProxyHeaders),
		SameSite: http.SameSiteLaxMode,
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":    "csrf token refreshed",
		"csrf_token": newCSRFToken,
	})
}

func (s *APIServer) sessionMaxAgeSeconds() int {
	if s == nil || s.sessionStore == nil || s.sessionStore.ttl <= 0 {
		return int((time.Duration(defaultSessionTTLMinutes) * time.Minute).Seconds())
	}
	return int(s.sessionStore.ttl.Seconds())
}

func isHTTPSRequest(req *http.Request, trustProxyHeaders bool) bool {
	if req == nil {
		return false
	}
	if req.TLS != nil {
		return true
	}
	if !trustProxyHeaders {
		return false
	}
	forwardedProto := strings.ToLower(strings.TrimSpace(req.Header.Get("X-Forwarded-Proto")))
	return forwardedProto == "https"
}

func (s *APIServer) GetSession(req *http.Request) *UserSession {
	cookie, err := req.Cookie("session")
	if err != nil {
		return nil
	}
	return s.sessionStore.GetSession(cookie.Value)
}

func (s *APIServer) GetAllModules(w http.ResponseWriter, req *http.Request) {
	session, ok := s.requireSession(w, req) // Koristimo s.requireSession da dobijemo sesiju
	if !ok {
		return
	}

	var appRoot *moduleNode
	groupNodes := make(map[string]moduleNode)
	moduleNodes := make(map[string]moduleNode)

	for _, moduleDef := range s.config.Modules { // Koristimo s.config
		node := moduleNode{
			ID:   moduleDef.ID,
			Name: moduleDef.Name,
			Type: moduleDef.Type,
		}

		switch moduleDef.Type {
		case ModuleTypeRoot:
			appRoot = &node
		case ModuleTypeGroup:
			groupNodes[moduleDef.ID] = node
		default:
			moduleNodes[moduleDef.ID] = node
		}
	}

	if appRoot == nil {
		appRoot = &moduleNode{
			ID:   "app_root",
			Name: "Aplikacija",
			Type: ModuleTypeRoot,
		}
	}

	// Sada gradimo hijerarhiju koristeći definicije iz s.config, ali proveravamo permisije preko s.canReadModule
	if appDef := s.config.GetModuleByID("app"); appDef != nil && appDef.Groups != nil { // Koristimo s.config
		for _, groupLink := range appDef.Groups {
			if groupNode, ok := groupNodes[groupLink.TargetGroupID]; ok {
				if groupDef := s.config.GetModuleByID(groupLink.TargetGroupID); groupDef != nil && groupDef.SubModules != nil { // Koristimo s.config
					for _, subModLink := range groupDef.SubModules {
						if !s.canReadModule(session.UserID, subModLink.TargetModuleID) {
							continue
						}
						if actualModuleNode, ok := moduleNodes[subModLink.TargetModuleID]; ok {
							groupNode.Children = append(groupNode.Children, actualModuleNode)
						} else {
							log.Printf("WARNING: Target modul '%s' za submodul '%s' (u grupi '%s') nije pronađen. Možda nedostaje JSON fajl?", subModLink.TargetModuleID, subModLink.DisplayName, groupLink.TargetGroupID)
						}
					}
				}
				if len(groupNode.Children) > 0 {
					appRoot.Children = append(appRoot.Children, groupNode)
				}
			} else {
				log.Printf("WARNING: Target grupa '%s' nije pronađena za grupu '%s' u root modulu. Možda nedostaje JSON fajl za grupu?", groupLink.TargetGroupID, groupLink.DisplayName)
			}
		}
	}

	if err := writeJSON(w, http.StatusOK, appRoot); err != nil {
		log.Printf("ERROR: Greška pri enkodiranju tree strukture modula: %v", err)
		return
	}
	log.Println("INFO: Vraćena tree struktura modula.")
}

func (s *APIServer) CreateUser(w http.ResponseWriter, req *http.Request) {
	session, ok := s.requireSession(w, req)
	if !ok {
		return
	}
	if !session.IsAdmin {
		writeJSONError(w, http.StatusForbidden, "forbidden: only admin can create users")
		return
	}

	var body struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
		IsAdmin  bool   `json:"is_admin"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.Username) == "" || strings.TrimSpace(body.Password) == "" {
		writeJSONError(w, http.StatusBadRequest, "username and password are required")
		return
	}

	hash, err := hashPassword(body.Password)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}

	id, err := s.dataset.CreateUser(body.Username, body.Email, hash, body.IsAdmin)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create user: %v", err))
		return
	}

	if err := writeJSON(w, http.StatusCreated, map[string]interface{}{
		"message": "user created",
		"id":      id,
	}); err != nil {
		log.Printf("ERROR: Greška pri enkodiranju odgovora: %v", err)
	}
}

func (s *APIServer) GetAuditLogs(w http.ResponseWriter, req *http.Request) {
	session, ok := s.requireSession(w, req)
	if !ok {
		return
	}

	if !session.IsAdmin {
		writeJSONError(w, http.StatusForbidden, "forbidden: audit access requires admin role")
		return
	}

	query := req.URL.Query()

	var actorUserID *int64
	if rawActorUserID := strings.TrimSpace(query.Get("actor_user_id")); rawActorUserID != "" {
		parsed, err := strconv.ParseInt(rawActorUserID, 10, 64)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid actor_user_id")
			return
		}
		actorUserID = &parsed
	}

	action := strings.TrimSpace(strings.ToLower(query.Get("action")))
	if action != "" && action != string(AuditActionCreate) && action != string(AuditActionUpdate) && action != string(AuditActionDelete) {
		writeJSONError(w, http.StatusBadRequest, "invalid action (allowed: create, update, delete)")
		return
	}

	fromTime, err := parseAuditTimeFilter(query.Get("from"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid from: %v", err))
		return
	}
	toTime, err := parseAuditTimeFilter(query.Get("to"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid to: %v", err))
		return
	}

	limit := 100
	if parsedLimit, hasLimit := parseNonNegativeIntParam(query, "limit", "_limit"); hasLimit {
		limit = parsedLimit
	}

	offset := 0
	if parsedOffset, hasOffset := parseNonNegativeIntParam(query, "offset", "_offset"); hasOffset {
		offset = parsedOffset
	}

	auditRows, err := s.dataset.GetAuditLogs(AuditQueryOptions{
		ModuleID:    strings.TrimSpace(query.Get("module_id")),
		RecordID:    strings.TrimSpace(query.Get("record_id")),
		ActorUserID: actorUserID,
		ActorName:   strings.TrimSpace(query.Get("actor_username")),
		Action:      action,
		FromTime:    fromTime,
		ToTime:      toTime,
		Limit:       limit,
		Offset:      offset,
		SortBy:      strings.TrimSpace(query.Get("sort_by")),
		SortDir:     strings.TrimSpace(query.Get("sort_dir")),
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to fetch audit logs: %v", err))
		return
	}

	if err := writeJSON(w, http.StatusOK, map[string]interface{}{
		"data": auditRows,
		"meta": map[string]interface{}{
			"limit":  limit,
			"offset": offset,
			"count":  len(auditRows),
		},
	}); err != nil {
		log.Printf("ERROR: Greška pri enkodiranju audit odgovora: %v", err)
	}
}

// GetModuleRecords handles requests to get records for a specific module.
func (s *APIServer) GetModuleRecords(w http.ResponseWriter, req *http.Request) { // Metoda APIServera
	session, ok := s.requireSession(w, req)
	if !ok {
		return
	}

	vars := mux.Vars(req)
	moduleID := vars["moduleID"]

	moduleDef := s.config.GetModuleByID(moduleID) // Koristimo s.config
	if moduleDef == nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("modul sa ID '%s' nije pronađen", moduleID))
		return
	}

	if ok := s.requireModulePermission(w, session.UserID, moduleID, PermissionRead); !ok {
		return
	}

	var records []map[string]interface{}
	var err error
	if moduleDef.SelectQuery != "" {
		records, err = s.dataset.GetReportData(moduleDef, req.URL.Query())
	} else {
		records, err = s.dataset.GetRecords(moduleDef, req.URL.Query())
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("greška pri dohvatanju zapisa za modul '%s': %v", moduleID, err))
		return
	}

	if err := writeJSON(w, http.StatusOK, records); err != nil {
		log.Printf("ERROR: Greška pri enkodiranju zapisa: %v", err)
		return
	}
	log.Printf("INFO: Vraćeno %d zapisa za modul '%s'.", len(records), moduleID)
}

// GetSingleRecord handles requests to get a single record by ID for a specific module.
func (s *APIServer) GetSingleRecord(w http.ResponseWriter, req *http.Request) { // Metoda APIServera
	session, ok := s.requireSession(w, req)
	if !ok {
		return
	}

	vars := mux.Vars(req)
	moduleID := vars["moduleID"]
	recordID := vars["recordID"]

	moduleDef := s.config.GetModuleByID(moduleID) // Koristimo s.config
	if moduleDef == nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("modul sa ID '%s' nije pronađen", moduleID))
		return
	}

	if ok := s.requireModulePermission(w, session.UserID, moduleID, PermissionRead); !ok {
		return
	}

	pkCol := s.dataset.getPrimaryKeyColumn(moduleDef) // Koristimo s.dataset
	var parsedRecordID interface{}
	if pkCol != nil && pkCol.Type == "integer" {
		id, err := strconv.Atoi(recordID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("nevažeći ID zapisa za modul '%s': %v", moduleID, err))
			return
		}
		parsedRecordID = id
	} else {
		parsedRecordID = recordID
	}

	record, err := s.dataset.GetRecordByID(moduleDef, parsedRecordID) // Koristimo s.dataset
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("greška pri dohvatanju zapisa sa ID '%s' za modul '%s': %v", recordID, moduleID, err))
		return
	}

	if err := writeJSON(w, http.StatusOK, record); err != nil {
		log.Printf("ERROR: Greška pri enkodiranju zapisa: %v", err)
		return
	}
	log.Printf("INFO: Vraćen zapis sa ID '%v' za modul '%s'.", parsedRecordID, moduleID)
}

func (s *APIServer) GetSubmoduleRecords(w http.ResponseWriter, req *http.Request) {
	session, ok := s.requireSession(w, req)
	if !ok {
		return
	}

	parentCtx, ok := s.resolveSubmoduleContext(w, req)
	if !ok {
		return
	}
	parentModule := parentCtx.ParentModule
	parentRecordID := parentCtx.ParentRecordID
	submoduleDef := parentCtx.Submodule
	childModule := parentCtx.ChildModule

	if ok := s.requireModulePermission(w, session.UserID, parentModule.ID, PermissionRead); !ok {
		return
	}
	if ok := s.requireModulePermission(w, session.UserID, childModule.ID, PermissionRead); !ok {
		return
	}

	queryParams := cloneQueryValues(req.URL.Query())
	queryParams.Set(submoduleDef.ChildForeignKeyField, fmt.Sprint(parentCtx.ParentKeyValue))

	records, err := s.dataset.GetRecords(childModule, queryParams)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("greška pri dohvatanju submodula '%s' za zapis '%s': %v", childModule.ID, parentRecordID, err))
		return
	}

	if err := writeJSON(w, http.StatusOK, map[string]interface{}{
		"parent_module_id": parentModule.ID,
		"parent_record_id": parentRecordID,
		"submodule":        submoduleMetadata(submoduleDef),
		"records":          records,
	}); err != nil {
		log.Printf("ERROR: Greška pri enkodiranju submodule odgovora: %v", err)
	}
}

func (s *APIServer) GetSubmoduleRecord(w http.ResponseWriter, req *http.Request) {
	session, ok := s.requireSession(w, req)
	if !ok {
		return
	}

	parentCtx, ok := s.resolveSubmoduleContext(w, req)
	if !ok {
		return
	}

	childRecordID := mux.Vars(req)["childRecordID"]
	parsedChildRecordID, err := s.parseRecordID(parentCtx.ChildModule, childRecordID)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	if ok := s.requireModulePermission(w, session.UserID, parentCtx.ParentModule.ID, PermissionRead); !ok {
		return
	}
	if ok := s.requireModulePermission(w, session.UserID, parentCtx.ChildModule.ID, PermissionRead); !ok {
		return
	}

	record, err := s.dataset.GetRecordByID(parentCtx.ChildModule, parsedChildRecordID)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("child zapis '%s' nije pronađen: %v", childRecordID, err))
		return
	}
	childParentKeyValue, err := s.dataset.GetRecordFieldByID(parentCtx.ChildModule, parsedChildRecordID, parentCtx.Submodule.ChildForeignKeyField)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("ne mogu da potvrdim parent pripadnost child zapisa '%s': %v", childRecordID, err))
		return
	}
	if fmt.Sprint(childParentKeyValue) != fmt.Sprint(parentCtx.ParentKeyValue) {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("child zapis '%s' ne pripada parent zapisu '%s'", childRecordID, parentCtx.ParentRecordID))
		return
	}

	if err := writeJSON(w, http.StatusOK, map[string]interface{}{
		"parent_module_id": parentCtx.ParentModule.ID,
		"parent_record_id": parentCtx.ParentRecordID,
		"submodule":        submoduleMetadata(parentCtx.Submodule),
		"record":           record,
	}); err != nil {
		log.Printf("ERROR: Greška pri enkodiranju submodule single odgovora: %v", err)
	}
}

func (s *APIServer) CreateSubmoduleRecord(w http.ResponseWriter, req *http.Request) {
	session, ok := s.requireSession(w, req)
	if !ok {
		return
	}

	parentCtx, ok := s.resolveSubmoduleContext(w, req)
	if !ok {
		return
	}
	parentRecordID := parentCtx.ParentRecordID
	submoduleDef := parentCtx.Submodule
	childModule := parentCtx.ChildModule

	if ok := s.requireModulePermission(w, session.UserID, childModule.ID, PermissionCreate); !ok {
		return
	}

	var payload map[string]interface{}
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("greška pri dekodiranju payload-a: %v", err))
		return
	}
	if payload == nil {
		payload = make(map[string]interface{})
	}
	payload[submoduleDef.ChildForeignKeyField] = parentCtx.ParentKeyValue

	if err := validatePayload(payload, childModule.Columns, s.config); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("greška validacije payload-a: %v", err))
		return
	}

	newID, err := s.dataset.CreateRecordWithForcedFields(childModule, payload, map[string]interface{}{submoduleDef.ChildForeignKeyField: parentCtx.ParentKeyValue})
	if err != nil {
		if errors.Is(err, ErrUniqueViolation) {
			writeJSONError(w, http.StatusConflict, fmt.Sprintf("greška pri kreiranju submodule zapisa '%s' za parent '%s': %v", childModule.ID, parentRecordID, err))
			return
		}
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("greška pri kreiranju submodule zapisa '%s' za parent '%s': %v", childModule.ID, parentRecordID, err))
		return
	}

	if s.isAuditEnabled() {
		var newRecord interface{} = map[string]interface{}{"id": newID}
		if record, fetchErr := s.dataset.GetRecordByID(childModule, newID); fetchErr == nil {
			newRecord = record
		}
		s.recordAuditEvent(session, childModule, AuditActionCreate, newID, nil, newRecord, requestIDFromContext(req))
	}

	if err := writeJSON(w, http.StatusCreated, map[string]interface{}{
		"message":          "submodule zapis uspešno kreiran",
		"id":               newID,
		"parent_record_id": parentRecordID,
		"submodule":        submoduleMetadata(submoduleDef),
	}); err != nil {
		log.Printf("ERROR: Greška pri enkodiranju CreateSubmodule odgovora: %v", err)
	}
}

func (s *APIServer) UpdateSubmoduleRecord(w http.ResponseWriter, req *http.Request) {
	session, ok := s.requireSession(w, req)
	if !ok {
		return
	}

	parentCtx, ok := s.resolveSubmoduleContext(w, req)
	if !ok {
		return
	}

	childRecordID := mux.Vars(req)["childRecordID"]
	parsedChildRecordID, err := s.parseRecordID(parentCtx.ChildModule, childRecordID)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	beforeRecord, err := s.dataset.GetRecordByID(parentCtx.ChildModule, parsedChildRecordID)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("child zapis '%s' nije pronađen: %v", childRecordID, err))
		return
	}
	childParentKeyValue, err := s.dataset.GetRecordFieldByID(parentCtx.ChildModule, parsedChildRecordID, parentCtx.Submodule.ChildForeignKeyField)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("ne mogu da potvrdim parent pripadnost child zapisa '%s': %v", childRecordID, err))
		return
	}
	if fmt.Sprint(childParentKeyValue) != fmt.Sprint(parentCtx.ParentKeyValue) {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("child zapis '%s' ne pripada parent zapisu '%s'", childRecordID, parentCtx.ParentRecordID))
		return
	}

	if ok := s.requireModulePermission(w, session.UserID, parentCtx.ChildModule.ID, PermissionUpdate); !ok {
		return
	}

	var payload map[string]interface{}
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("greška pri dekodiranju payload-a: %v", err))
		return
	}
	if payload == nil {
		payload = make(map[string]interface{})
	}
	payload[parentCtx.Submodule.ChildForeignKeyField] = parentCtx.ParentKeyValue

	if err := validatePayload(payload, parentCtx.ChildModule.Columns, s.config); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("greška validacije payload-a: %v", err))
		return
	}

	if err := s.dataset.UpdateRecordWithForcedFields(parentCtx.ChildModule, childRecordID, payload, map[string]interface{}{parentCtx.Submodule.ChildForeignKeyField: parentCtx.ParentKeyValue}); err != nil {
		if errors.Is(err, ErrUniqueViolation) {
			writeJSONError(w, http.StatusConflict, fmt.Sprintf("greška pri ažuriranju submodule zapisa '%s': %v", childRecordID, err))
			return
		}
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("greška pri ažuriranju submodule zapisa '%s': %v", childRecordID, err))
		return
	}

	if s.isAuditEnabled() {
		var afterRecord interface{} = map[string]interface{}{"id": parsedChildRecordID}
		if record, fetchErr := s.dataset.GetRecordByID(parentCtx.ChildModule, parsedChildRecordID); fetchErr == nil {
			afterRecord = record
		}
		s.recordAuditEvent(session, parentCtx.ChildModule, AuditActionUpdate, parsedChildRecordID, beforeRecord, afterRecord, requestIDFromContext(req))
	}

	if err := writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":          "submodule zapis uspešno ažuriran",
		"child_record_id":  childRecordID,
		"parent_record_id": parentCtx.ParentRecordID,
		"submodule":        submoduleMetadata(parentCtx.Submodule),
	}); err != nil {
		log.Printf("ERROR: Greška pri enkodiranju UpdateSubmodule odgovora: %v", err)
	}
}

func (s *APIServer) DeleteSubmoduleRecord(w http.ResponseWriter, req *http.Request) {
	session, ok := s.requireSession(w, req)
	if !ok {
		return
	}

	parentCtx, ok := s.resolveSubmoduleContext(w, req)
	if !ok {
		return
	}

	childRecordID := mux.Vars(req)["childRecordID"]
	parsedChildRecordID, err := s.parseRecordID(parentCtx.ChildModule, childRecordID)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	beforeRecord, err := s.dataset.GetRecordByID(parentCtx.ChildModule, parsedChildRecordID)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("child zapis '%s' nije pronađen: %v", childRecordID, err))
		return
	}
	childParentKeyValue, err := s.dataset.GetRecordFieldByID(parentCtx.ChildModule, parsedChildRecordID, parentCtx.Submodule.ChildForeignKeyField)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("ne mogu da potvrdim parent pripadnost child zapisa '%s': %v", childRecordID, err))
		return
	}
	if fmt.Sprint(childParentKeyValue) != fmt.Sprint(parentCtx.ParentKeyValue) {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("child zapis '%s' ne pripada parent zapisu '%s'", childRecordID, parentCtx.ParentRecordID))
		return
	}

	if ok := s.requireModulePermission(w, session.UserID, parentCtx.ChildModule.ID, PermissionDelete); !ok {
		return
	}

	if err := s.dataset.DeleteRecord(parentCtx.ChildModule, childRecordID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("greška pri brisanju submodule zapisa '%s': %v", childRecordID, err))
		return
	}

	if s.isAuditEnabled() {
		s.recordAuditEvent(session, parentCtx.ChildModule, AuditActionDelete, parsedChildRecordID, beforeRecord, nil, requestIDFromContext(req))
	}

	if err := writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":          "submodule zapis uspešno obrisan",
		"child_record_id":  childRecordID,
		"parent_record_id": parentCtx.ParentRecordID,
		"submodule":        submoduleMetadata(parentCtx.Submodule),
	}); err != nil {
		log.Printf("ERROR: Greška pri enkodiranju DeleteSubmodule odgovora: %v", err)
	}
}

// CreateRecord handles requests to create a new record for a module.
func (s *APIServer) CreateRecord(w http.ResponseWriter, req *http.Request) { // Metoda APIServera
	// Provera sesije i dozvola
	session, ok := s.requireSession(w, req)
	if !ok {
		return
	}

	vars := mux.Vars(req)
	moduleID := vars["moduleID"]

	moduleDef := s.config.GetModuleByID(moduleID) // Koristimo s.config
	if moduleDef == nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("modul sa ID '%s' nije pronađen", moduleID))
		return
	}

	// Provera dozvole CREATE
	if ok := s.requireModulePermission(w, session.UserID, moduleID, PermissionCreate); !ok {
		return
	}

	var payload map[string]interface{}
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("greška pri dekodiranju payload-a: %v", err))
		return
	}

	// Validacija payload-a - validatePayload je i dalje samostalna funkcija, ali joj prosleđujemo s.config
	if err := validatePayload(payload, moduleDef.Columns, s.config); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("greška validacije payload-a: %v", err))
		return
	}

	newID, err := s.dataset.CreateRecord(moduleDef, payload) // Koristimo s.dataset
	if err != nil {
		if errors.Is(err, ErrUniqueViolation) {
			writeJSONError(w, http.StatusConflict, fmt.Sprintf("greška pri kreiranju zapisa za modul '%s': %v", moduleID, err))
			return
		}
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("greška pri kreiranju zapisa za modul '%s': %v", moduleID, err))
		return
	}

	if s.isAuditEnabled() {
		var newRecord interface{} = map[string]interface{}{"id": newID}
		if record, fetchErr := s.dataset.GetRecordByID(moduleDef, newID); fetchErr == nil {
			newRecord = record
		}
		s.recordAuditEvent(session, moduleDef, AuditActionCreate, newID, nil, newRecord, requestIDFromContext(req))
	}

	if err := writeJSON(w, http.StatusCreated, map[string]interface{}{"message": "zapis uspešno kreiran", "id": newID}); err != nil {
		log.Printf("ERROR: Greška pri enkodiranju odgovora za CreateRecord: %v", err)
	}
	log.Printf("INFO: Kreiran zapis sa ID '%v' za modul '%s'.", newID, moduleID)
}

// UpdateRecord handles requests to update an existing record for a module.
func (s *APIServer) UpdateRecord(w http.ResponseWriter, req *http.Request) { // Metoda APIServera
	// Provera sesije i dozvola
	session, ok := s.requireSession(w, req)
	if !ok {
		return
	}

	vars := mux.Vars(req)
	moduleID := vars["moduleID"]
	recordID := vars["recordID"]

	moduleDef := s.config.GetModuleByID(moduleID) // Koristimo s.config
	if moduleDef == nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("modul sa ID '%s' nije pronađen", moduleID))
		return
	}

	var parsedRecordID interface{} = recordID
	if s.isAuditEnabled() {
		if parsed, parseErr := s.parseRecordID(moduleDef, recordID); parseErr == nil {
			parsedRecordID = parsed
		}
	}

	var beforeRecord interface{}
	if s.isAuditEnabled() {
		if record, fetchErr := s.dataset.GetRecordByID(moduleDef, parsedRecordID); fetchErr == nil {
			beforeRecord = record
		}
	}

	// Provera dozvole UPDATE
	if ok := s.requireModulePermission(w, session.UserID, moduleID, PermissionUpdate); !ok {
		return
	}

	var payload map[string]interface{}
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("greška pri dekodiranju payload-a: %v", err))
		return
	}

	// Validacija payload-a
	if err := validatePayload(payload, moduleDef.Columns, s.config); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("greška validacije payload-a: %v", err))
		return
	}

	if err := s.dataset.UpdateRecord(moduleDef, recordID, payload); err != nil { // Koristimo s.dataset
		if errors.Is(err, ErrUniqueViolation) {
			writeJSONError(w, http.StatusConflict, fmt.Sprintf("greška pri ažuriranju zapisa sa ID '%s' za modul '%s': %v", recordID, moduleID, err))
			return
		}
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("greška pri ažuriranju zapisa sa ID '%s' za modul '%s': %v", recordID, moduleID, err))
		return
	}

	if s.isAuditEnabled() {
		var afterRecord interface{} = map[string]interface{}{"id": parsedRecordID}
		if record, fetchErr := s.dataset.GetRecordByID(moduleDef, parsedRecordID); fetchErr == nil {
			afterRecord = record
		}
		s.recordAuditEvent(session, moduleDef, AuditActionUpdate, parsedRecordID, beforeRecord, afterRecord, requestIDFromContext(req))
	}

	if err := writeJSON(w, http.StatusOK, map[string]string{"message": "zapis uspešno ažuriran"}); err != nil {
		log.Printf("ERROR: Greška pri enkodiranju odgovora za UpdateRecord: %v", err)
	}
	log.Printf("INFO: Ažuriran zapis sa ID '%s' za modul '%s'.", recordID, moduleID)
}

// DeleteRecord handles requests to delete an existing record for a module.
func (s *APIServer) DeleteRecord(w http.ResponseWriter, req *http.Request) { // Metoda APIServera
	// Provera sesije i dozvola
	session, ok := s.requireSession(w, req)
	if !ok {
		return
	}

	vars := mux.Vars(req)
	moduleID := vars["moduleID"]
	recordID := vars["recordID"]

	moduleDef := s.config.GetModuleByID(moduleID) // Koristimo s.config
	if moduleDef == nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("modul sa ID '%s' nije pronađen", moduleID))
		return
	}

	var parsedRecordID interface{} = recordID
	if s.isAuditEnabled() {
		if parsed, parseErr := s.parseRecordID(moduleDef, recordID); parseErr == nil {
			parsedRecordID = parsed
		}
	}

	var beforeRecord interface{}
	if s.isAuditEnabled() {
		if record, fetchErr := s.dataset.GetRecordByID(moduleDef, parsedRecordID); fetchErr == nil {
			beforeRecord = record
		}
	}

	// Provera dozvole DELETE
	if ok := s.requireModulePermission(w, session.UserID, moduleID, PermissionDelete); !ok {
		return
	}

	if err := s.dataset.DeleteRecord(moduleDef, recordID); err != nil { // Koristimo s.dataset
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("greška pri brisanju zapisa sa ID '%s' za modul '%s': %v", recordID, moduleID, err))
		return
	}

	if s.isAuditEnabled() {
		s.recordAuditEvent(session, moduleDef, AuditActionDelete, parsedRecordID, beforeRecord, nil, requestIDFromContext(req))
	}

	if err := writeJSON(w, http.StatusOK, map[string]string{"message": "zapis uspešno obrisan"}); err != nil {
		log.Printf("ERROR: Greška pri enkodiranju odgovora za DeleteRecord: %v", err)
	}
	log.Printf("INFO: Obrisan zapis sa ID '%s' za modul '%s'.", recordID, moduleID)
}

func decodeLoginRequest(req *http.Request) (loginRequest, error) {
	var payload loginRequest
	contentType := strings.ToLower(req.Header.Get("Content-Type"))
	if strings.Contains(contentType, "application/json") {
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			return payload, fmt.Errorf("invalid JSON body: %w", err)
		}
		return payload, nil
	}

	if err := req.ParseForm(); err != nil {
		return payload, fmt.Errorf("invalid form body: %w", err)
	}
	payload.Username = req.FormValue("username")
	payload.Password = req.FormValue("password")
	return payload, nil
}

func parseAuditTimeFilter(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}

	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.Format(time.RFC3339), nil
	}

	if parsed, err := time.Parse("2006-01-02", value); err == nil {
		return parsed.Format("2006-01-02 15:04:05"), nil
	}

	return "", fmt.Errorf("expected RFC3339 or YYYY-MM-DD")
}

func (s *APIServer) requireSession(w http.ResponseWriter, req *http.Request) (*UserSession, bool) {
	session := s.GetSession(req)
	if session == nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return nil, false
	}
	return session, true
}

func (s *APIServer) requireModulePermission(w http.ResponseWriter, userID int64, moduleID string, permission int64) bool {
	has, err := s.dataset.HasPermission(userID, moduleID, permission)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("permission check failed: %v", err))
		return false
	}
	if !has {
		writeJSONError(w, http.StatusForbidden, fmt.Sprintf("forbidden: missing permission %d on resource '%s'", permission, moduleID))
		return false
	}
	return true
}

func (s *APIServer) canReadModule(userID int64, moduleID string) bool {
	has, err := s.dataset.HasPermission(userID, moduleID, PermissionRead)
	if err != nil {
		log.Printf("WARNING: READ permission check failed for module '%s': %v", moduleID, err)
		return false
	}
	return has
}

type submoduleContext struct {
	ParentModule   *ModuleDefinition
	ParentRecordID string
	Submodule      *SubModuleDefinition
	ChildModule    *ModuleDefinition
	ParentKeyValue interface{}
}

func (s *APIServer) resolveSubmoduleContext(w http.ResponseWriter, req *http.Request) (*submoduleContext, bool) {
	vars := mux.Vars(req)
	moduleID := vars["moduleID"]
	recordID := vars["recordID"]
	submoduleID := vars["submoduleID"]

	parentModule := s.config.GetModuleByID(moduleID)
	if parentModule == nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("modul sa ID '%s' nije pronađen", moduleID))
		return nil, false
	}

	submoduleDef := s.findSubmoduleDefinition(parentModule, submoduleID)
	if submoduleDef == nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("submodule '%s' nije definisan za modul '%s'", submoduleID, moduleID))
		return nil, false
	}

	childModule := s.config.GetModuleByID(submoduleDef.TargetModuleID)
	if childModule == nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("target modul '%s' nije pronađen za submodule '%s'", submoduleDef.TargetModuleID, submoduleID))
		return nil, false
	}

	parentKeyValue, err := s.getParentKeyValue(parentModule, recordID, submoduleDef)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "nije pronađen") || strings.Contains(err.Error(), "nevažeći") {
			status = http.StatusBadRequest
		}
		if strings.Contains(err.Error(), "nije pronađen u modulu") {
			status = http.StatusNotFound
		}
		writeJSONError(w, status, err.Error())
		return nil, false
	}

	return &submoduleContext{
		ParentModule:   parentModule,
		ParentRecordID: recordID,
		Submodule:      submoduleDef,
		ChildModule:    childModule,
		ParentKeyValue: parentKeyValue,
	}, true
}

func (s *APIServer) getParentKeyValue(parentModule *ModuleDefinition, recordID string, submoduleDef *SubModuleDefinition) (interface{}, error) {
	parsedRecordID, err := s.parseRecordID(parentModule, recordID)
	if err != nil {
		return nil, err
	}

	joinField := submoduleDef.ParentKeyField
	if joinField == "" {
		pkCol := s.dataset.getPrimaryKeyColumn(parentModule)
		if pkCol == nil {
			return nil, fmt.Errorf("modul '%s' nema definisan primarni ključ", parentModule.ID)
		}
		joinField = pkCol.DBColumnName
	}

	value, err := s.dataset.GetRecordFieldByID(parentModule, parsedRecordID, joinField)
	if err != nil {
		return nil, err
	}

	if value == nil {
		return nil, fmt.Errorf("parent join polje '%s' je prazno za zapis '%s' u modulu '%s'", joinField, recordID, parentModule.ID)
	}

	return value, nil
}

func (s *APIServer) parseRecordID(moduleDef *ModuleDefinition, recordID string) (interface{}, error) {
	pkCol := s.dataset.getPrimaryKeyColumn(moduleDef)
	if pkCol != nil && pkCol.Type == "integer" {
		id, err := strconv.Atoi(recordID)
		if err != nil {
			return nil, fmt.Errorf("nevažeći ID zapisa za modul '%s': %v", moduleDef.ID, err)
		}
		return id, nil
	}
	return recordID, nil
}

func (s *APIServer) findSubmoduleDefinition(moduleDef *ModuleDefinition, submoduleID string) *SubModuleDefinition {
	for i := range moduleDef.SubModules {
		submodule := &moduleDef.SubModules[i]
		if submodule.ID == submoduleID || submodule.TargetModuleID == submoduleID {
			return submodule
		}
	}
	return nil
}

func recordBelongsToParent(childRecord map[string]interface{}, fkField string, parentKeyValue interface{}) bool {
	if childRecord == nil {
		return false
	}
	fkValue, ok := childRecord[fkField]
	if !ok || fkValue == nil {
		return false
	}
	return fmt.Sprint(fkValue) == fmt.Sprint(parentKeyValue)
}

func cloneQueryValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for key, value := range values {
		copied := make([]string, len(value))
		copy(copied, value)
		cloned[key] = copied
	}
	return cloned
}

func submoduleMetadata(submodule *SubModuleDefinition) map[string]interface{} {
	return map[string]interface{}{
		"id":                      submodule.ID,
		"display_name":            submodule.DisplayName,
		"parent_key_field":        submodule.ParentKeyField,
		"target_module_id":        submodule.TargetModuleID,
		"child_foreign_key_field": submodule.ChildForeignKeyField,
		"display_order":           submodule.DisplayOrder,
	}
}

func (s *APIServer) isAuditEnabled() bool {
	return s != nil && s.dataset != nil && s.dataset.auditEnable
}

func (s *APIServer) recordAuditEvent(session *UserSession, moduleDef *ModuleDefinition, action AuditAction, recordID interface{}, oldData interface{}, newData interface{}, requestID string) {
	if session == nil || moduleDef == nil || !s.isAuditEnabled() {
		return
	}
	if strings.TrimSpace(requestID) == "" {
		requestID = nextRequestID()
	}

	evt := AuditEvent{
		ModuleID:      moduleDef.ID,
		RecordID:      fmt.Sprint(recordID),
		Action:        action,
		ActorUserID:   session.UserID,
		ActorUsername: session.Username,
		RequestID:     requestID,
		OldData:       oldData,
		NewData:       newData,
	}

	if err := s.dataset.LogAuditEvent(evt); err != nil {
		log.Printf("WARNING: Audit upis nije uspeo (module=%s, record=%v, action=%s): %v", moduleDef.ID, recordID, action, err)
	}
}

type apiErrorResponse struct {
	Error     string      `json:"error"`
	Code      string      `json:"code"`
	Message   string      `json:"message"`
	Details   interface{} `json:"details"`
	RequestID string      `json:"request_id"`
}

var requestIDCounter uint64

func nextRequestID() string {
	n := atomic.AddUint64(&requestIDCounter, 1)
	return fmt.Sprintf("req_%d_%d", time.Now().UnixNano(), n)
}

func requestIDFromContext(req *http.Request) string {
	if req == nil {
		return ""
	}
	if requestID, ok := req.Context().Value(requestIDContextKey).(string); ok {
		return strings.TrimSpace(requestID)
	}
	return ""
}

func errorCodeFromStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusServiceUnavailable:
		return "service_unavailable"
	default:
		if status >= 500 {
			return "internal_error"
		}
		return "error"
	}
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	requestID := strings.TrimSpace(w.Header().Get(requestIDHeader))
	if requestID == "" {
		requestID = nextRequestID()
		w.Header().Set(requestIDHeader, requestID)
	}

	resp := apiErrorResponse{
		Error:     message,
		Code:      errorCodeFromStatus(status),
		Message:   message,
		Details:   nil,
		RequestID: requestID,
	}

	if err := writeJSON(w, status, resp); err != nil {
		log.Printf("ERROR: writeJSONError failed: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(payload)
}
