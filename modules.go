// models.go
package main

type ModuleType string

const (
	ModuleTypeModule ModuleType = "module"
	ModuleTypeReport ModuleType = "report"
	ModuleTypeGroup  ModuleType = "group"
	ModuleTypeRoot   ModuleType = "root"
	ModuleTypeSystem ModuleType = "system"
	ModuleTypeCustom ModuleType = "custom"
)

// ColumnDefinition defines the structure of a column in a module.
type ColumnDefinition struct {
	ID                 string      `json:"id"`
	Name               string      `json:"name"`
	Type               string      `json:"type"` // e.g., "string", "integer", "float", "boolean", "date", "datetime", "lookup"
	DBColumnName       string      `json:"db_column_name"`
	IsPrimaryKey       bool        `json:"is_primary_key"`
	IsUnique           bool        `json:"is_unique"`
	IsSearchable       bool        `json:"is_searchable"`
	IsSortable         bool        `json:"is_sortable"`
	IsVisible          bool        `json:"is_visible"`
	IsEditable         bool        `json:"is_editable"`
	IsReadOnly         bool        `json:"is_read_only"`         // Dodato za read-only polja (npr. auto-increment ID)
	Validation         string      `json:"validation"`           // e.g., "required,min:5,max:100,email,regex:^[A-Za-z]+$"
	DefaultValue       interface{} `json:"default_value"`        // Defaultna vrednost za kreiranje
	LookupModuleID     string      `json:"lookup_module_id"`     // ID modula za lookup polja
	LookupDisplayField string      `json:"lookup_display_field"` // Polje iz lookup modula koje se prikazuje
	// Runtime fields (populated during app initialization)
	LookupModule *ModuleDefinition `json:"-"` // Pointer to the actual ModuleDefinition for lookup
}

// SubModuleDefinition defines a submodule relationship.
type SubModuleDefinition struct {
	ID                   string `json:"id"`
	DisplayName          string `json:"display_name"`
	ParentKeyField       string `json:"parent_key_field"`
	TargetModuleID       string `json:"target_module_id"`        // ID modula koji predstavlja submodule
	ChildForeignKeyField string `json:"child_foreign_key_field"` // Polje u target modulu koje referencira primarni ključ roditelja
	DisplayOrder         int    `json:"display_order"`
	// Runtime fields
	TargetModule *ModuleDefinition `json:"-"` // Pointer to the actual ModuleDefinition for the target module
}

// ModuleDefinition defines the structure of a data module.
type ModuleDefinition struct {
	ID           string                 `json:"id"`
	Name         string                 `json:"name"`
	Type         ModuleType             `json:"type"` // e.g., module, group, root, report, custom
	Description  string                 `json:"description"`
	CanRead      bool                   `json:"can_read"`
	CanCreate    bool                   `json:"can_create"`
	CanUpdate    bool                   `json:"can_update"`
	CanDelete    bool                   `json:"can_delete"`
	CanExecute   bool                   `json:"can_execute"`
	CanExport    bool                   `json:"can_export"`
	CanImport    bool                   `json:"can_import"`
	CanApprove   bool                   `json:"can_approve"`
	DBTableName  string                 `json:"db_table_name"` // Used for modules backed by database tables
	DisplayField string                 `json:"display_field"` // Field to display in lists (e.g., "name" or "title")
	SelectQuery  string                 `json:"select_query"`  // Used for "report" or "custom" type modules
	Columns      []ColumnDefinition     `json:"columns"`
	SubModules   []SubModuleDefinition  `json:"sub_modules"`
	Properties   map[string]interface{} `json:"properties,omitempty"` // Dodaj ako već nema
	Groups       []GroupLink            `json:"groups,omitempty"`     // <-- NOVO: Dodaj ovo polje za "app" modul
	IsTable      bool                   `json:"is_table"`             // Whether this module is backed by a database table
}

// GroupLink defines a link to a group, used within the root module (e.g., app.json)
type GroupLink struct {
	TargetGroupID string `json:"target_group_id"`
	Type          string `json:"type"` // e.g., "group" or "group_link"
	DisplayName   string `json:"display_name"`
	DisplayOrder  int    `json:"display_order"`
}

// Permission bitmask konstante
const (
	PermissionRead    int64 = 1 << 0 // 1
	PermissionCreate  int64 = 1 << 1 // 2
	PermissionUpdate  int64 = 1 << 2 // 4
	PermissionDelete  int64 = 1 << 3 // 8
	PermissionExecute int64 = 1 << 4 // 16
	PermissionExport  int64 = 1 << 5 // 32
	PermissionImport  int64 = 1 << 6 // 64
	PermissionApprove int64 = 1 << 7 // 128
)

// User predstavlja logovanog korisnika
type User struct {
	ID           int64  `json:"id" db:"id"`
	Username     string `json:"username" db:"username"`
	PasswordHash string `json:"-" db:"password_hash"`
	IsAdmin      bool   `json:"is_admin" db:"is_admin"`
	CreatedAt    string `json:"created_at" db:"created_at"`
}

// Role predstavlja ulogu
type Role struct {
	ID   int64  `json:"id" db:"id"`
	Name string `json:"name" db:"name"`
}

// RolePermission predstavlja dozvole za role na resursu
type RolePermission struct {
	ID          int64  `json:"id" db:"id"`
	RoleID      int64  `json:"role_id" db:"role_id"`
	Resource    string `json:"resource" db:"resource"` // modul_id
	Permissions int64  `json:"permissions" db:"permissions"`
}

// UserSession predstavlja aktivnu sesiju
type UserSession struct {
	UserID   int64
	Username string
	IsAdmin  bool
}

func (m *ModuleDefinition) IsPermissionResource() bool {
	if m == nil {
		return false
	}

	switch m.Type {
	case ModuleTypeModule, ModuleTypeReport, ModuleTypeSystem, ModuleTypeCustom:
		return true
	default:
		return false
	}
}

func (m *ModuleDefinition) SupportedPermissions() int64 {
	if m == nil || !m.IsPermissionResource() {
		return 0
	}

	var perms int64
	if m.CanRead {
		perms |= PermissionRead
	}
	if m.CanCreate {
		perms |= PermissionCreate
	}
	if m.CanUpdate {
		perms |= PermissionUpdate
	}
	if m.CanDelete {
		perms |= PermissionDelete
	}
	if m.CanExecute {
		perms |= PermissionExecute
	}
	if m.CanExport {
		perms |= PermissionExport
	}
	if m.CanImport {
		perms |= PermissionImport
	}
	if m.CanApprove {
		perms |= PermissionApprove
	}

	return perms
}

func (m *ModuleDefinition) DefaultUserPermissions() int64 {
	if m == nil || !m.IsPermissionResource() {
		return 0
	}

	if m.Type == ModuleTypeSystem {
		return 0
	}

	var perms int64
	if m.CanRead {
		perms |= PermissionRead
	}
	if m.IsTable && m.CanCreate {
		perms |= PermissionCreate
	}

	return perms
}
