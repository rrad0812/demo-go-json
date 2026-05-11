package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestGetAllModulesRequiresSession(t *testing.T) {
	server, cleanup := newModuleAuthTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/modules", nil)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGetModuleRecordsReturnsForbiddenWithoutReadPermission(t *testing.T) {
	server, mock, token, cleanup := newModuleAuthTestServerWithSession(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT required_permissions FROM resources WHERE resource = $1")).
		WithArgs("module_orders").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT is_admin FROM users WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(99).
		WillReturnRows(sqlmock.NewRows([]string{"is_admin"}).AddRow(false))
	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT rp.permissions FROM role_permissions rp
		JOIN user_roles ur ON rp.role_id = ur.role_id
		WHERE ur.user_id = $1 AND rp.resource = $2
	`)).
		WithArgs(99, "module_orders").
		WillReturnRows(sqlmock.NewRows([]string{"permissions"}))

	req := httptest.NewRequest(http.MethodGet, "/api/modules/module_orders", nil)
	attachSessionAuth(req, server, token)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGetModuleRecordsReturnsRecords(t *testing.T) {
	server, mock, token, cleanup := newModuleCRUDTestServerWithSession(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT required_permissions FROM resources WHERE resource = $1")).
		WithArgs("module_orders").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT is_admin FROM users WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(99).
		WillReturnRows(sqlmock.NewRows([]string{"is_admin"}).AddRow(false))
	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT rp.permissions FROM role_permissions rp
		JOIN user_roles ur ON rp.role_id = ur.role_id
		WHERE ur.user_id = $1 AND rp.resource = $2
	`)).
		WithArgs(99, "module_orders").
		WillReturnRows(sqlmock.NewRows([]string{"permissions"}).AddRow(int64(PermissionRead)))

	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM orders WHERE is_deleted = FALSE")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "order_number"}).AddRow(55, "ORD-001"))

	req := httptest.NewRequest(http.MethodGet, "/api/modules/module_orders", nil)
	attachSessionAuth(req, server, token)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var response []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(response) != 1 {
		t.Fatalf("expected 1 record, got %d", len(response))
	}
	if response[0]["order_number"] != "ORD-001" {
		t.Fatalf("expected order_number ORD-001, got %v", response[0]["order_number"])
	}
}

func TestGetModuleRecordsSupportsPaginationAndSortAliases(t *testing.T) {
	server, mock, token, cleanup := newModuleCRUDTestServerWithSession(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT required_permissions FROM resources WHERE resource = $1")).
		WithArgs("module_orders").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT is_admin FROM users WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(99).
		WillReturnRows(sqlmock.NewRows([]string{"is_admin"}).AddRow(false))
	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT rp.permissions FROM role_permissions rp
		JOIN user_roles ur ON rp.role_id = ur.role_id
		WHERE ur.user_id = $1 AND rp.resource = $2
	`)).
		WithArgs(99, "module_orders").
		WillReturnRows(sqlmock.NewRows([]string{"permissions"}).AddRow(int64(PermissionRead)))

	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM orders WHERE is_deleted = FALSE ORDER BY order_number DESC LIMIT $1 OFFSET $2")).
		WithArgs(5, 10).
		WillReturnRows(sqlmock.NewRows([]string{"id", "order_number"}).AddRow(55, "ORD-001"))

	req := httptest.NewRequest(http.MethodGet, "/api/modules/module_orders?limit=5&offset=10&sort_by=order_number&sort_dir=desc", nil)
	attachSessionAuth(req, server, token)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGetModuleRecordsIgnoresNonWhitelistedSortColumn(t *testing.T) {
	server, mock, token, cleanup := newModuleCRUDTestServerWithSession(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT required_permissions FROM resources WHERE resource = $1")).
		WithArgs("module_orders").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT is_admin FROM users WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(99).
		WillReturnRows(sqlmock.NewRows([]string{"is_admin"}).AddRow(false))
	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT rp.permissions FROM role_permissions rp
		JOIN user_roles ur ON rp.role_id = ur.role_id
		WHERE ur.user_id = $1 AND rp.resource = $2
	`)).
		WithArgs(99, "module_orders").
		WillReturnRows(sqlmock.NewRows([]string{"permissions"}).AddRow(int64(PermissionRead)))

	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM orders WHERE is_deleted = FALSE")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "order_number"}).AddRow(55, "ORD-001"))

	req := httptest.NewRequest(http.MethodGet, "/api/modules/module_orders?sort_by=not_a_column&sort_dir=desc", nil)
	attachSessionAuth(req, server, token)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGetSingleRecordReturnsRecord(t *testing.T) {
	server, mock, token, cleanup := newModuleCRUDTestServerWithSession(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT required_permissions FROM resources WHERE resource = $1")).
		WithArgs("module_orders").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT is_admin FROM users WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(99).
		WillReturnRows(sqlmock.NewRows([]string{"is_admin"}).AddRow(false))
	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT rp.permissions FROM role_permissions rp
		JOIN user_roles ur ON rp.role_id = ur.role_id
		WHERE ur.user_id = $1 AND rp.resource = $2
	`)).
		WithArgs(99, "module_orders").
		WillReturnRows(sqlmock.NewRows([]string{"permissions"}).AddRow(int64(PermissionRead)))

	mock.ExpectQuery(regexp.QuoteMeta("SELECT id AS id, order_number AS order_number FROM orders WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(55).
		WillReturnRows(sqlmock.NewRows([]string{"id", "order_number"}).AddRow(55, "ORD-001"))

	req := httptest.NewRequest(http.MethodGet, "/api/modules/module_orders/55", nil)
	attachSessionAuth(req, server, token)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	response := decodeJSONResponse(t, rr.Body.String())
	if response["id"] != float64(55) {
		t.Fatalf("expected id 55, got %v", response["id"])
	}
	if response["order_number"] != "ORD-001" {
		t.Fatalf("expected order_number ORD-001, got %v", response["order_number"])
	}
}

func TestCreateRecordRequiresSession(t *testing.T) {
	server, cleanup := newModuleAuthTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/modules/module_orders", nil)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestCreateRecordReturnsForbiddenWithoutCreatePermission(t *testing.T) {
	server, mock, token, cleanup := newModuleAuthTestServerWithSession(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT required_permissions FROM resources WHERE resource = $1")).
		WithArgs("module_orders").
		WillReturnError(sql.ErrNoRows)

	req := httptest.NewRequest(http.MethodPost, "/api/modules/module_orders", nil)
	attachSessionAuth(req, server, token)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestUpdateRecordRequiresSession(t *testing.T) {
	server, cleanup := newModuleAuthTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPut, "/api/modules/module_orders/10", nil)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestUpdateRecordReturnsForbiddenWithoutUpdatePermission(t *testing.T) {
	server, mock, token, cleanup := newModuleAuthTestServerWithSession(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT required_permissions FROM resources WHERE resource = $1")).
		WithArgs("module_orders").
		WillReturnError(sql.ErrNoRows)

	req := httptest.NewRequest(http.MethodPut, "/api/modules/module_orders/10", nil)
	attachSessionAuth(req, server, token)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestDeleteRecordRequiresSession(t *testing.T) {
	server, cleanup := newModuleAuthTestServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodDelete, "/api/modules/module_orders/10", nil)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestDeleteRecordReturnsForbiddenWithoutDeletePermission(t *testing.T) {
	server, mock, token, cleanup := newModuleAuthTestServerWithSession(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT required_permissions FROM resources WHERE resource = $1")).
		WithArgs("module_orders").
		WillReturnError(sql.ErrNoRows)

	req := httptest.NewRequest(http.MethodDelete, "/api/modules/module_orders/10", nil)
	attachSessionAuth(req, server, token)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestCreateRecordCreatesModuleRecord(t *testing.T) {
	server, mock, token, cleanup := newModuleCRUDTestServerWithSession(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT required_permissions FROM resources WHERE resource = $1")).
		WithArgs("module_orders").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT is_admin FROM users WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(99).
		WillReturnRows(sqlmock.NewRows([]string{"is_admin"}).AddRow(false))
	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT rp.permissions FROM role_permissions rp
		JOIN user_roles ur ON rp.role_id = ur.role_id
		WHERE ur.user_id = $1 AND rp.resource = $2
	`)).
		WithArgs(99, "module_orders").
		WillReturnRows(sqlmock.NewRows([]string{"permissions"}).AddRow(int64(PermissionCreate)))

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO orders (order_number, created_at, updated_at) VALUES ($1, $2, $3) RETURNING id")).
		WithArgs("ORD-001", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(55))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodPost, "/api/modules/module_orders", strings.NewReader(`{"order_number":"ORD-001"}`))
	attachSessionAuth(req, server, token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rr.Code, rr.Body.String())
	}

	if response := decodeJSONResponse(t, rr.Body.String()); response["id"] != float64(55) {
		t.Fatalf("expected id 55, got %v", response["id"])
	}
}

func TestUpdateRecordUpdatesModuleRecord(t *testing.T) {
	server, mock, token, cleanup := newModuleCRUDTestServerWithSession(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT required_permissions FROM resources WHERE resource = $1")).
		WithArgs("module_orders").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT is_admin FROM users WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(99).
		WillReturnRows(sqlmock.NewRows([]string{"is_admin"}).AddRow(false))
	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT rp.permissions FROM role_permissions rp
		JOIN user_roles ur ON rp.role_id = ur.role_id
		WHERE ur.user_id = $1 AND rp.resource = $2
	`)).
		WithArgs(99, "module_orders").
		WillReturnRows(sqlmock.NewRows([]string{"permissions"}).AddRow(int64(PermissionUpdate)))

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE orders SET order_number = $1, updated_at = $2 WHERE id = $3")).
		WithArgs("ORD-002", sqlmock.AnyArg(), "55").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodPut, "/api/modules/module_orders/55", strings.NewReader(`{"order_number":"ORD-002"}`))
	attachSessionAuth(req, server, token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	if response := decodeJSONResponse(t, rr.Body.String()); response["message"] != "zapis uspešno ažuriran" {
		t.Fatalf("expected update message, got %v", response["message"])
	}
}

func TestDeleteRecordDeletesModuleRecord(t *testing.T) {
	server, mock, token, cleanup := newModuleCRUDTestServerWithSession(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT required_permissions FROM resources WHERE resource = $1")).
		WithArgs("module_orders").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT is_admin FROM users WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(99).
		WillReturnRows(sqlmock.NewRows([]string{"is_admin"}).AddRow(false))
	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT rp.permissions FROM role_permissions rp
		JOIN user_roles ur ON rp.role_id = ur.role_id
		WHERE ur.user_id = $1 AND rp.resource = $2
	`)).
		WithArgs(99, "module_orders").
		WillReturnRows(sqlmock.NewRows([]string{"permissions"}).AddRow(int64(PermissionDelete)))

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE orders SET is_deleted = TRUE WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs("55").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodDelete, "/api/modules/module_orders/55", nil)
	attachSessionAuth(req, server, token)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	if response := decodeJSONResponse(t, rr.Body.String()); response["message"] != "zapis uspešno obrisan" {
		t.Fatalf("expected delete message, got %v", response["message"])
	}
}

func newModuleAuthTestServer(t *testing.T) (*APIServer, func()) {
	t.Helper()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock db: %v", err)
	}

	config := &AppConfig{Modules: map[string]*ModuleDefinition{
		"module_orders": {
			ID:          "module_orders",
			Name:        "Narudžbine",
			Type:        "module",
			IsTable:     true,
			DBTableName: "orders",
			CanRead:     true,
			Columns: []ColumnDefinition{
				{ID: "orders_id", Name: "ID", Type: "integer", DBColumnName: "id", IsPrimaryKey: true, IsVisible: false},
			},
		},
	}}

	server := NewAPIServer(config, &SQLDataset{db: db, config: config})

	cleanup := func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet sql expectations: %v", err)
		}
	}

	return server, cleanup
}

func newModuleAuthTestServerWithSession(t *testing.T) (*APIServer, sqlmock.Sqlmock, string, func()) {
	t.Helper()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock db: %v", err)
	}

	config := &AppConfig{Modules: map[string]*ModuleDefinition{
		"module_orders": {
			ID:          "module_orders",
			Name:        "Narudžbine",
			Type:        "module",
			IsTable:     true,
			DBTableName: "orders",
			CanRead:     true,
			Columns: []ColumnDefinition{
				{ID: "orders_id", Name: "ID", Type: "integer", DBColumnName: "id", IsPrimaryKey: true, IsVisible: false},
			},
		},
	}}

	server := NewAPIServer(config, &SQLDataset{db: db, config: config})
	token := server.sessionStore.CreateSession(&User{ID: 99, Username: "tester", IsAdmin: false})

	cleanup := func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet sql expectations: %v", err)
		}
	}

	return server, mock, token, cleanup
}

func newModuleCRUDTestServerWithSession(t *testing.T) (*APIServer, sqlmock.Sqlmock, string, func()) {
	t.Helper()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock db: %v", err)
	}

	config := &AppConfig{Modules: map[string]*ModuleDefinition{
		"module_orders": {
			ID:          "module_orders",
			Name:        "Narudžbine",
			Type:        "module",
			IsTable:     true,
			DBTableName: "orders",
			CanRead:     true,
			CanCreate:   true,
			CanUpdate:   true,
			CanDelete:   true,
			Columns: []ColumnDefinition{
				{ID: "orders_id", Name: "ID", Type: "integer", DBColumnName: "id", IsPrimaryKey: true, IsVisible: false},
				{ID: "orders_number", Name: "Broj", Type: "string", DBColumnName: "order_number", IsVisible: true, IsEditable: true},
			},
		},
	}}

	server := NewAPIServer(config, &SQLDataset{db: db, config: config})
	token := server.sessionStore.CreateSession(&User{ID: 99, Username: "tester", IsAdmin: false})

	cleanup := func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet sql expectations: %v", err)
		}
	}

	return server, mock, token, cleanup
}

func decodeJSONResponse(t *testing.T, body string) map[string]interface{} {
	t.Helper()

	var response map[string]interface{}
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	return response
}

func attachSessionAuth(req *http.Request, server *APIServer, token string) {
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	if server != nil && server.sessionStore != nil {
		if csrfToken := server.sessionStore.GetCSRFToken(token); csrfToken != "" {
			req.Header.Set("X-CSRF-Token", csrfToken)
		}
	}
}
