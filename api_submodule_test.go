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

func TestGetSubmoduleRecords(t *testing.T) {
	server, mock, token, cleanup := newNestedSubmoduleTestServer(t)
	defer cleanup()

	expectNestedReadPermissions(mock)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM orders WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))

	rows := sqlmock.NewRows([]string{"id", "order_id", "name"}).
		AddRow(10, 1, "First item")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM order_items WHERE order_id = $1 AND is_deleted = FALSE")).
		WithArgs(1).
		WillReturnRows(rows)

	req := httptest.NewRequest(http.MethodGet, "/api/modules/module_orders/1/submodules/module_order_items", nil)
	attachNestedSessionAuth(req, server, token)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var response map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if got := response["parent_module_id"]; got != "module_orders" {
		t.Fatalf("expected parent_module_id module_orders, got %v", got)
	}
	if got := response["parent_record_id"]; got != "1" {
		t.Fatalf("expected parent_record_id 1, got %v", got)
	}
	if got := response["records"]; got == nil {
		t.Fatalf("expected records in response")
	}

	records := response["records"].([]interface{})
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	record := records[0].(map[string]interface{})
	if record["name"] != "First item" {
		t.Fatalf("expected record name First item, got %v", record["name"])
	}
	if record["order_id"] != float64(1) {
		t.Fatalf("expected record order_id 1, got %v", record["order_id"])
	}

}

func TestGetSubmoduleRecord(t *testing.T) {
	server, mock, token, cleanup := newNestedSubmoduleTestServer(t)
	defer cleanup()

	expectNestedReadPermissions(mock)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM orders WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))

	mock.ExpectQuery(regexp.QuoteMeta("SELECT id AS id, name AS name FROM order_items WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(10).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow(10, "Hidden FK item"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT order_id FROM order_items WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(10).
		WillReturnRows(sqlmock.NewRows([]string{"order_id"}).AddRow(1))
	req := httptest.NewRequest(http.MethodGet, "/api/modules/module_orders/1/submodules/module_order_items/10", nil)
	attachNestedSessionAuth(req, server, token)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var response map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if got := response["parent_module_id"]; got != "module_orders" {
		t.Fatalf("expected parent_module_id module_orders, got %v", got)
	}
	record := response["record"].(map[string]interface{})
	if record["name"] != "Hidden FK item" {
		t.Fatalf("expected record name Hidden FK item, got %v", record["name"])
	}
	if _, ok := record["order_id"]; ok {
		t.Fatalf("expected hidden fk order_id to be omitted from record payload")
	}

}

func TestGetSubmoduleRecordReturnsForbiddenWithoutChildRead(t *testing.T) {
	server, mock, token, cleanup := newNestedSubmoduleTestServer(t)
	defer cleanup()

	parentReadPermissionQuery := regexp.QuoteMeta(`
		SELECT rp.permissions FROM role_permissions rp
		JOIN user_roles ur ON rp.role_id = ur.role_id
		WHERE ur.user_id = $1 AND rp.resource = $2
	`)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT required_permissions FROM resources WHERE resource = $1")).
		WithArgs("module_orders").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT required_permissions FROM resources WHERE resource = $1")).
		WithArgs("module_order_items").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT is_admin FROM users WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(99).
		WillReturnRows(sqlmock.NewRows([]string{"is_admin"}).AddRow(false))
	mock.ExpectQuery(parentReadPermissionQuery).
		WithArgs(99, "module_orders").
		WillReturnRows(sqlmock.NewRows([]string{"permissions"}).AddRow(int64(PermissionRead)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT is_admin FROM users WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(99).
		WillReturnRows(sqlmock.NewRows([]string{"is_admin"}).AddRow(false))
	mock.ExpectQuery(parentReadPermissionQuery).
		WithArgs(99, "module_order_items").
		WillReturnRows(sqlmock.NewRows([]string{"permissions"}))

	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM orders WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))

	req := httptest.NewRequest(http.MethodGet, "/api/modules/module_orders/1/submodules/module_order_items/10", nil)
	attachNestedSessionAuth(req, server, token)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d: %s", rr.Code, rr.Body.String())
	}

	var response map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if response["error"] == nil {
		t.Fatalf("expected error response")
	}
}

func TestGetSubmoduleRecordReturnsBadRequestWhenChildDoesNotBelongToParent(t *testing.T) {
	server, mock, token, cleanup := newNestedSubmoduleTestServer(t)
	defer cleanup()

	expectNestedReadPermissions(mock)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM orders WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id AS id, name AS name FROM order_items WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(10).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow(10, "Mismatch item"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT order_id FROM order_items WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(10).
		WillReturnRows(sqlmock.NewRows([]string{"order_id"}).AddRow(999))

	req := httptest.NewRequest(http.MethodGet, "/api/modules/module_orders/1/submodules/module_order_items/10", nil)
	attachNestedSessionAuth(req, server, token)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", rr.Code, rr.Body.String())
	}

	var response map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if response["error"] == nil {
		t.Fatalf("expected error response")
	}
}

func TestCreateSubmoduleRecord(t *testing.T) {
	server, mock, token, cleanup := newNestedSubmoduleTestServer(t)
	defer cleanup()

	expectSingleSubmoduleWritePermission(mock, "module_order_items", PermissionCreate)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM orders WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO order_items (order_id, name, created_at, updated_at) VALUES ($1, $2, $3, $4) RETURNING id")).
		WithArgs(1, "New nested item", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(77))
	mock.ExpectCommit()

	body := `{"name":"New nested item"}`
	req := httptest.NewRequest(http.MethodPost, "/api/modules/module_orders/1/submodules/module_order_items", strings.NewReader(body))
	attachNestedSessionAuth(req, server, token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var response map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if response["id"] != float64(77) {
		t.Fatalf("expected id 77, got %v", response["id"])
	}
	if response["parent_record_id"] != "1" {
		t.Fatalf("expected parent_record_id 1, got %v", response["parent_record_id"])
	}

}

func TestCreateSubmoduleRecordReturnsForbiddenWithoutPermission(t *testing.T) {
	server, mock, token, cleanup := newNestedSubmoduleTestServer(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM orders WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	expectSingleSubmoduleWritePermissionDenied(mock, "module_order_items")

	body := `{"name":"New nested item"}`
	req := httptest.NewRequest(http.MethodPost, "/api/modules/module_orders/1/submodules/module_order_items", strings.NewReader(body))
	attachNestedSessionAuth(req, server, token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d: %s", rr.Code, rr.Body.String())
	}

	var response map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if response["error"] == nil {
		t.Fatalf("expected error response")
	}
}

func TestUpdateSubmoduleRecord(t *testing.T) {
	server, mock, token, cleanup := newNestedSubmoduleTestServer(t)
	defer cleanup()

	expectSingleSubmoduleWritePermission(mock, "module_order_items", PermissionUpdate)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM orders WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id AS id, name AS name FROM order_items WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(10).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow(10, "Old item"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT order_id FROM order_items WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(10).
		WillReturnRows(sqlmock.NewRows([]string{"order_id"}).AddRow(1))
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE order_items SET order_id = $1, name = $2, updated_at = $3 WHERE id = $4")).
		WithArgs(1, "Updated item", sqlmock.AnyArg(), "10").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	body := `{"name":"Updated item"}`
	req := httptest.NewRequest(http.MethodPut, "/api/modules/module_orders/1/submodules/module_order_items/10", strings.NewReader(body))
	attachNestedSessionAuth(req, server, token)
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

	if response["child_record_id"] != "10" {
		t.Fatalf("expected child_record_id 10, got %v", response["child_record_id"])
	}
	if response["parent_record_id"] != "1" {
		t.Fatalf("expected parent_record_id 1, got %v", response["parent_record_id"])
	}

}

func TestUpdateSubmoduleRecordReturnsForbiddenWithoutPermission(t *testing.T) {
	server, mock, token, cleanup := newNestedSubmoduleTestServer(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM orders WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id AS id, name AS name FROM order_items WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(10).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow(10, "Old item"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT order_id FROM order_items WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(10).
		WillReturnRows(sqlmock.NewRows([]string{"order_id"}).AddRow(1))
	expectSingleSubmoduleWritePermissionDenied(mock, "module_order_items")

	body := `{"name":"Updated item"}`
	req := httptest.NewRequest(http.MethodPut, "/api/modules/module_orders/1/submodules/module_order_items/10", strings.NewReader(body))
	attachNestedSessionAuth(req, server, token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d: %s", rr.Code, rr.Body.String())
	}

	var response map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if response["error"] == nil {
		t.Fatalf("expected error response")
	}
}

func TestDeleteSubmoduleRecord(t *testing.T) {
	server, mock, token, cleanup := newNestedSubmoduleTestServer(t)
	defer cleanup()

	expectSingleSubmoduleWritePermission(mock, "module_order_items", PermissionDelete)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM orders WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id AS id, name AS name FROM order_items WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(10).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow(10, "Delete item"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT order_id FROM order_items WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(10).
		WillReturnRows(sqlmock.NewRows([]string{"order_id"}).AddRow(1))
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE order_items SET is_deleted = TRUE WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs("10").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodDelete, "/api/modules/module_orders/1/submodules/module_order_items/10", nil)
	attachNestedSessionAuth(req, server, token)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var response map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if response["child_record_id"] != "10" {
		t.Fatalf("expected child_record_id 10, got %v", response["child_record_id"])
	}
	if response["parent_record_id"] != "1" {
		t.Fatalf("expected parent_record_id 1, got %v", response["parent_record_id"])
	}

}

func TestDeleteSubmoduleRecordReturnsForbiddenWithoutPermission(t *testing.T) {
	server, mock, token, cleanup := newNestedSubmoduleTestServer(t)
	defer cleanup()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM orders WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id AS id, name AS name FROM order_items WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(10).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow(10, "Delete item"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT order_id FROM order_items WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(10).
		WillReturnRows(sqlmock.NewRows([]string{"order_id"}).AddRow(1))
	expectSingleSubmoduleWritePermissionDenied(mock, "module_order_items")

	req := httptest.NewRequest(http.MethodDelete, "/api/modules/module_orders/1/submodules/module_order_items/10", nil)
	attachNestedSessionAuth(req, server, token)
	rr := httptest.NewRecorder()

	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d: %s", rr.Code, rr.Body.String())
	}

	var response map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if response["error"] == nil {
		t.Fatalf("expected error response")
	}
}

func newNestedSubmoduleTestServer(t *testing.T) (*APIServer, sqlmock.Sqlmock, string, func()) {
	t.Helper()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock db: %v", err)
	}
	mock.MatchExpectationsInOrder(false)

	parentModule := &ModuleDefinition{
		ID:          "module_orders",
		Name:        "Narudžbine",
		Type:        "module",
		IsTable:     true,
		DBTableName: "orders",
		CanRead:     true,
		Columns: []ColumnDefinition{
			{ID: "orders_id", Name: "ID", Type: "integer", DBColumnName: "id", IsPrimaryKey: true, IsVisible: false},
			{ID: "orders_number", Name: "Broj", Type: "string", DBColumnName: "order_number", IsVisible: true, IsEditable: true},
		},
		SubModules: []SubModuleDefinition{
			{
				ID:                   "order_items",
				DisplayName:          "Stavke narudžbine",
				ParentKeyField:       "id",
				TargetModuleID:       "module_order_items",
				ChildForeignKeyField: "order_id",
				DisplayOrder:         1,
			},
		},
	}

	childModule := &ModuleDefinition{
		ID:          "module_order_items",
		Name:        "Stavke Narudžbine",
		Type:        "module",
		IsTable:     true,
		DBTableName: "order_items",
		CanRead:     true,
		CanCreate:   true,
		CanUpdate:   true,
		CanDelete:   true,
		Columns: []ColumnDefinition{
			{ID: "order_items_id", Name: "ID", Type: "integer", DBColumnName: "id", IsPrimaryKey: true, IsVisible: false},
			{ID: "order_items_order_id", Name: "Order", Type: "integer", DBColumnName: "order_id", IsVisible: false},
			{ID: "order_items_name", Name: "Naziv", Type: "string", DBColumnName: "name", IsVisible: true, IsEditable: true},
		},
	}

	cfg := &AppConfig{
		Modules: map[string]*ModuleDefinition{
			parentModule.ID: parentModule,
			childModule.ID:  childModule,
		},
	}

	dataset := &SQLDataset{db: db, config: cfg}
	server := NewAPIServer(cfg, dataset)
	token := server.sessionStore.CreateSession(&User{ID: 99, Username: "tester", IsAdmin: false})

	cleanup := func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet sql expectations: %v", err)
		}
	}

	return server, mock, token, cleanup
}

func expectNestedReadPermissions(mock sqlmock.Sqlmock) {
	readPermissionQuery := regexp.QuoteMeta(`
		SELECT rp.permissions FROM role_permissions rp
		JOIN user_roles ur ON rp.role_id = ur.role_id
		WHERE ur.user_id = $1 AND rp.resource = $2
	`)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT required_permissions FROM resources WHERE resource = $1")).
		WithArgs("module_orders").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT required_permissions FROM resources WHERE resource = $1")).
		WithArgs("module_order_items").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT is_admin FROM users WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(99).
		WillReturnRows(sqlmock.NewRows([]string{"is_admin"}).AddRow(false))
	mock.ExpectQuery(readPermissionQuery).
		WithArgs(99, "module_orders").
		WillReturnRows(sqlmock.NewRows([]string{"permissions"}).AddRow(int64(PermissionRead)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT is_admin FROM users WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(99).
		WillReturnRows(sqlmock.NewRows([]string{"is_admin"}).AddRow(false))
	mock.ExpectQuery(readPermissionQuery).
		WithArgs(99, "module_order_items").
		WillReturnRows(sqlmock.NewRows([]string{"permissions"}).AddRow(int64(PermissionRead)))
}

func expectSingleSubmoduleWritePermission(mock sqlmock.Sqlmock, resource string, permission int64) {
	permissionQuery := regexp.QuoteMeta(`
		SELECT rp.permissions FROM role_permissions rp
		JOIN user_roles ur ON rp.role_id = ur.role_id
		WHERE ur.user_id = $1 AND rp.resource = $2
	`)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT required_permissions FROM resources WHERE resource = $1")).
		WithArgs(resource).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT is_admin FROM users WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(99).
		WillReturnRows(sqlmock.NewRows([]string{"is_admin"}).AddRow(false))
	mock.ExpectQuery(permissionQuery).
		WithArgs(99, resource).
		WillReturnRows(sqlmock.NewRows([]string{"permissions"}).AddRow(int64(permission)))
}

func expectSingleSubmoduleWritePermissionDenied(mock sqlmock.Sqlmock, resource string) {
	permissionQuery := regexp.QuoteMeta(`
		SELECT rp.permissions FROM role_permissions rp
		JOIN user_roles ur ON rp.role_id = ur.role_id
		WHERE ur.user_id = $1 AND rp.resource = $2
	`)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT required_permissions FROM resources WHERE resource = $1")).
		WithArgs(resource).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT is_admin FROM users WHERE id = $1 AND is_deleted = FALSE")).
		WithArgs(99).
		WillReturnRows(sqlmock.NewRows([]string{"is_admin"}).AddRow(false))
	mock.ExpectQuery(permissionQuery).
		WithArgs(99, resource).
		WillReturnRows(sqlmock.NewRows([]string{"permissions"}).AddRow(int64(0)))
}

func attachNestedSessionAuth(req *http.Request, server *APIServer, token string) {
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	if server != nil && server.sessionStore != nil {
		if csrfToken := server.sessionStore.GetCSRFToken(token); csrfToken != "" {
			req.Header.Set("X-CSRF-Token", csrfToken)
		}
	}
}
