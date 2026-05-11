package main

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestGetAuditLogsRequiresSession(t *testing.T) {
	server, cleanup := newAuditTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/audit", nil)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGetAuditLogsRequiresAdmin(t *testing.T) {
	server, _, userToken, _, cleanup := newAuditTestServerWithSessions(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/audit", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: userToken})
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGetAuditLogsReturnsRowsForAdmin(t *testing.T) {
	server, mock, _, adminToken, cleanup := newAuditTestServerWithSessions(t)
	defer cleanup()

	mock.ExpectQuery(`SELECT id, module_id, record_id, action,[\s\S]*FROM audit_log[\s\S]*ORDER BY created_at DESC LIMIT \$1`).
		WithArgs(100).
		WillReturnRows(sqlmock.NewRows([]string{"id", "module_id", "record_id", "action", "actor_user_id", "actor_username", "request_id", "old_data", "new_data", "created_at"}).
			AddRow(1, "module_orders", "55", "update", 99, "admin", "req_1", nil, nil, "2026-05-10T10:00:00Z"))

	req := httptest.NewRequest(http.MethodGet, "/api/audit", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: adminToken})
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGetAuditLogsFiltersByModuleID(t *testing.T) {
	server, mock, _, adminToken, cleanup := newAuditTestServerWithSessions(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT id, module_id, record_id, action,
		       actor_user_id, actor_username, request_id,
		       old_data, new_data, created_at
		FROM audit_log
		 WHERE module_id = $1 ORDER BY created_at DESC LIMIT $2`)).
		WithArgs("module_orders", 100).
		WillReturnRows(sqlmock.NewRows([]string{"id", "module_id", "record_id", "action", "actor_user_id", "actor_username", "request_id", "old_data", "new_data", "created_at"}).
			AddRow(2, "module_orders", "56", "create", 99, "admin", "req_2", nil, nil, "2026-05-10T11:00:00Z"))

	req := httptest.NewRequest(http.MethodGet, "/api/audit?module_id=module_orders", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: adminToken})
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGetAuditLogsFiltersByActionAndDateRange(t *testing.T) {
	server, mock, _, adminToken, cleanup := newAuditTestServerWithSessions(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT id, module_id, record_id, action,
		       actor_user_id, actor_username, request_id,
		       old_data, new_data, created_at
		FROM audit_log
		 WHERE action = $1 AND created_at >= $2 AND created_at <= $3 ORDER BY created_at DESC LIMIT $4`)).
		WithArgs("update", "2026-05-01 00:00:00", "2026-05-10 00:00:00", 100).
		WillReturnRows(sqlmock.NewRows([]string{"id", "module_id", "record_id", "action", "actor_user_id", "actor_username", "request_id", "old_data", "new_data", "created_at"}).
			AddRow(3, "module_orders", "57", "update", 99, "admin", "req_3", nil, nil, "2026-05-09T09:30:00Z"))

	req := httptest.NewRequest(http.MethodGet, "/api/audit?action=update&from=2026-05-01&to=2026-05-10", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: adminToken})
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func newAuditTestServer(t *testing.T) (*APIServer, func()) {
	t.Helper()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock db: %v", err)
	}

	cfg := &AppConfig{Modules: map[string]*ModuleDefinition{}}
	dataset := &SQLDataset{db: db, config: cfg, auditEnable: false}
	server := NewAPIServer(cfg, dataset)

	cleanup := func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet sql expectations: %v", err)
		}
	}

	return server, cleanup
}

func newAuditTestServerWithSessions(t *testing.T) (*APIServer, sqlmock.Sqlmock, string, string, func()) {
	t.Helper()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock db: %v", err)
	}

	cfg := &AppConfig{Modules: map[string]*ModuleDefinition{}}
	dataset := &SQLDataset{db: db, config: cfg, auditEnable: true}
	server := NewAPIServer(cfg, dataset)

	userToken := server.sessionStore.CreateSession(&User{ID: 101, Username: "user", IsAdmin: false})
	adminToken := server.sessionStore.CreateSession(&User{ID: 99, Username: "admin", IsAdmin: true})

	cleanup := func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet sql expectations: %v", err)
		}
	}

	return server, mock, userToken, adminToken, cleanup
}
