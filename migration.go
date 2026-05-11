// migration.go
package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"unicode"
)

var migrationDryRun bool

func SetMigrationDryRun(enabled bool) {
	migrationDryRun = enabled
}

func isMigrationDryRun() bool {
	return migrationDryRun
}

type dryRunSQLResult struct{}

func (dryRunSQLResult) LastInsertId() (int64, error) {
	return 0, nil
}

func (dryRunSQLResult) RowsAffected() (int64, error) {
	return 0, nil
}

func execMigration(db *sql.DB, query string, args ...interface{}) (sql.Result, error) {
	if isMigrationDryRun() {
		log.Printf("MIGRATION DRY-RUN: SQL=%s ARGS=%v", strings.TrimSpace(query), args)
		return dryRunSQLResult{}, nil
	}
	return db.Exec(query, args...)
}

// goTypeToSQL mapira tipove iz ModuleDefinition na PostgreSQL tipove.
func goTypeToSQL(colType string) string {
	switch colType {
	case "integer", "lookup":
		return "INTEGER"
	case "float":
		return "NUMERIC"
	case "boolean":
		return "BOOLEAN"
	case "date":
		return "DATE"
	case "datetime":
		return "TIMESTAMP"
	default: // "string" i sve ostalo
		return "TEXT"
	}
}

// columnsHash računa SHA256 hash definicije kolona modula.
// Koristi se za otkrivanje promena u JSON def fajlovima.
func columnsHash(columns []ColumnDefinition) (string, error) {
	data, err := json.Marshal(columns)
	if err != nil {
		return "", fmt.Errorf("greška pri serijalizaciji kolona: %w", err)
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum), nil
}

// ensureMigrationsTable kreira tabelu za praćenje migracija ako ne postoji.
func ensureMigrationsTable(db *sql.DB) error {
	_, err := execMigration(db, `
		CREATE TABLE IF NOT EXISTS _schema_migrations (
			module_id    TEXT PRIMARY KEY,
			table_name   TEXT        NOT NULL,
			columns_hash TEXT        NOT NULL,
			applied_at   TIMESTAMP   NOT NULL DEFAULT NOW()
		)
	`)
	if err != nil {
		return fmt.Errorf("greška pri kreiranju _schema_migrations tabele: %w", err)
	}
	return nil
}

// getStoredHash vraća hash koji je sačuvan u _schema_migrations za dati modul.
// Vraća ("", nil) ako zapis ne postoji.
func getStoredHash(db *sql.DB, moduleID string) (string, error) {
	var hash string
	err := db.QueryRow(
		`SELECT columns_hash FROM _schema_migrations WHERE module_id = $1`, moduleID,
	).Scan(&hash)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("greška pri čitanju hash-a za modul '%s': %w", moduleID, err)
	}
	return hash, nil
}

// upsertMigrationRecord čuva/ažurira hash za dati modul.
func upsertMigrationRecord(db *sql.DB, moduleID, tableName, hash string) error {
	_, err := execMigration(db, `
		INSERT INTO _schema_migrations (module_id, table_name, columns_hash, applied_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (module_id) DO UPDATE
			SET columns_hash = EXCLUDED.columns_hash,
			    table_name   = EXCLUDED.table_name,
			    applied_at   = NOW()
	`, moduleID, tableName, hash)
	if err != nil {
		return fmt.Errorf("greška pri upisivanju migracije za modul '%s': %w", moduleID, err)
	}
	return nil
}

// getExistingDBColumns vraća mapu db_column_name -> SQL tip za kolone koje već postoje u tabeli.
func getExistingDBColumns(db *sql.DB, tableName string) (map[string]string, error) {
	rows, err := db.Query(`
		SELECT column_name, data_type
		FROM information_schema.columns
		WHERE table_name = $1
		  AND table_schema = 'public'
	`, tableName)
	if err != nil {
		return nil, fmt.Errorf("greška pri čitanju kolona tabele '%s': %w", tableName, err)
	}
	defer rows.Close()

	cols := make(map[string]string)
	for rows.Next() {
		var colName, dataType string
		if err := rows.Scan(&colName, &dataType); err != nil {
			return nil, err
		}
		cols[colName] = strings.ToUpper(dataType)
	}
	return cols, rows.Err()
}

// createTable kreira novu tabelu na osnovu ModuleDefinition.
func createTable(db *sql.DB, mod *ModuleDefinition) error {
	var colDefs []string
	hasSoftDeleteCol := false
	for _, col := range mod.Columns {
		if col.DBColumnName == "" {
			continue
		}
		if col.DBColumnName == "is_deleted" {
			hasSoftDeleteCol = true
		}
		sqlType := goTypeToSQL(col.Type)
		var def string
		if col.IsPrimaryKey {
			def = fmt.Sprintf("%s SERIAL PRIMARY KEY", col.DBColumnName)
		} else {
			def = fmt.Sprintf("%s %s", col.DBColumnName, sqlType)
		}
		colDefs = append(colDefs, def)
	}

	if !hasSoftDeleteCol {
		colDefs = append(colDefs, "is_deleted BOOLEAN NOT NULL DEFAULT FALSE")
	}

	if len(colDefs) == 0 {
		return fmt.Errorf("modul '%s' nema kolona za kreiranje tabele", mod.ID)
	}

	query := fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s (\n\t%s\n)",
		mod.DBTableName,
		strings.Join(colDefs, ",\n\t"),
	)
	log.Printf("MIGRATION: Kreiram tabelu '%s':\n%s", mod.DBTableName, query)
	if _, err := execMigration(db, query); err != nil {
		return fmt.Errorf("greška pri kreiranju tabele '%s': %w", mod.DBTableName, err)
	}
	log.Printf("MIGRATION: Tabela '%s' uspešno kreirana.", mod.DBTableName)
	return nil
}

// managedColumns su kolone koje sistem automatski dodaje i njima upravlja.
var managedColumns = map[string]bool{
	"is_deleted": true,
	"created_at": true,
	"updated_at": true,
}

func ensureSoftDeleteColumn(db *sql.DB, tableName string, existingCols map[string]string) error {
	if _, exists := existingCols["is_deleted"]; exists {
		return nil
	}

	query := fmt.Sprintf("ALTER TABLE %s ADD COLUMN is_deleted BOOLEAN NOT NULL DEFAULT FALSE", tableName)
	log.Printf("MIGRATION: Dodajem soft-delete kolonu u tabelu '%s': %s", tableName, query)
	if _, err := execMigration(db, query); err != nil {
		return fmt.Errorf("greška pri dodavanju is_deleted kolone u tabelu '%s': %w", tableName, err)
	}

	return nil
}

func ensureAuditColumns(db *sql.DB, tableName string, existingCols map[string]string) error {
	audit := []struct {
		name string
		ddl  string
	}{
		{"created_at", fmt.Sprintf("ALTER TABLE %s ADD COLUMN created_at TIMESTAMP NOT NULL DEFAULT NOW()", tableName)},
		{"updated_at", fmt.Sprintf("ALTER TABLE %s ADD COLUMN updated_at TIMESTAMP NOT NULL DEFAULT NOW()", tableName)},
	}

	for _, col := range audit {
		if _, exists := existingCols[col.name]; exists {
			continue
		}
		log.Printf("MIGRATION: Dodajem audit kolonu '%s' u tabelu '%s'", col.name, tableName)
		if _, err := execMigration(db, col.ddl); err != nil {
			return fmt.Errorf("greška pri dodavanju audit kolone '%s' u tabelu '%s': %w", col.name, tableName, err)
		}
	}

	return nil
}

// alterTable poredi kolone iz JSON def-a sa stvarnim kolonama u bazi
// i dodaje nove kolone putem ALTER TABLE.
// Uklonjene ili kolone sa promenjenim tipom se ne brišu — samo se loguje upozorenje.
func alterTable(db *sql.DB, mod *ModuleDefinition, existingCols map[string]string) error {
	for _, col := range mod.Columns {
		if col.DBColumnName == "" || col.IsPrimaryKey {
			continue
		}
		if _, exists := existingCols[col.DBColumnName]; exists {
			// Kolona već postoji — proveri da li se tip promenio (samo upozorenje)
			dbType := existingCols[col.DBColumnName]
			expectedType := goTypeToSQL(col.Type)
			// PostgreSQL vraća "integer", "text", itd. — normalizujemo za poređenje
			if !sqlTypesCompatible(dbType, expectedType) {
				log.Printf(
					"MIGRATION WARNING: Kolona '%s.%s' ima tip '%s' u bazi, ali '%s' u definiciji. Tip se neće menjati automatski!",
					mod.DBTableName, col.DBColumnName, dbType, expectedType,
				)
			}
			continue
		}

		// Nova kolona — dodaj je
		sqlType := goTypeToSQL(col.Type)
		query := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", mod.DBTableName, col.DBColumnName, sqlType)
		log.Printf("MIGRATION: Dodajem kolonu '%s' u tabelu '%s': %s", col.DBColumnName, mod.DBTableName, query)
		if _, err := execMigration(db, query); err != nil {
			return fmt.Errorf("greška pri dodavanju kolone '%s' u tabelu '%s': %w", col.DBColumnName, mod.DBTableName, err)
		}
		log.Printf("MIGRATION: Kolona '%s.%s' uspešno dodata.", mod.DBTableName, col.DBColumnName)
	}

	// Upozorenje za kolone koje su u bazi ali ne i u definiciji
	jsonCols := make(map[string]bool)
	for _, col := range mod.Columns {
		if col.DBColumnName != "" {
			jsonCols[col.DBColumnName] = true
		}
	}
	for dbCol := range existingCols {
		if managedColumns[dbCol] {
			continue
		}
		if !jsonCols[dbCol] {
			log.Printf(
				"MIGRATION WARNING: Kolona '%s.%s' postoji u bazi ali nije u JSON definiciji. Neće biti uklonjena automatski.",
				mod.DBTableName, dbCol,
			)
		}
	}

	return nil
}

func sanitizeIdentifierPart(in string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(in) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	out := b.String()
	if out == "" {
		return "x"
	}
	return out
}

func uniqueIndexName(tableName, columnName string) string {
	return fmt.Sprintf("uq_%s_%s", sanitizeIdentifierPart(tableName), sanitizeIdentifierPart(columnName))
}

func fkConstraintName(tableName, columnName string) string {
	return fmt.Sprintf("fk_%s_%s", sanitizeIdentifierPart(tableName), sanitizeIdentifierPart(columnName))
}

func getManagedUniqueIndexes(db *sql.DB, tableName string) (map[string]bool, error) {
	rows, err := db.Query(`
		SELECT indexname
		FROM pg_indexes
		WHERE schemaname = 'public'
		  AND tablename = $1
		  AND indexname LIKE $2
	`, tableName, fmt.Sprintf("uq_%s_%%", sanitizeIdentifierPart(tableName)))
	if err != nil {
		return nil, fmt.Errorf("greška pri čitanju unique indeksa za tabelu '%s': %w", tableName, err)
	}
	defer rows.Close()

	idx := make(map[string]bool)
	for rows.Next() {
		var indexName string
		if err := rows.Scan(&indexName); err != nil {
			return nil, err
		}
		idx[indexName] = true
	}

	return idx, rows.Err()
}

func syncUniqueIndexes(db *sql.DB, mod *ModuleDefinition) error {
	existing, err := getManagedUniqueIndexes(db, mod.DBTableName)
	if err != nil {
		return err
	}

	expected := make(map[string]bool)
	for _, col := range mod.Columns {
		if col.DBColumnName == "" || col.IsPrimaryKey {
			continue
		}

		if !col.IsUnique {
			continue
		}

		idxName := uniqueIndexName(mod.DBTableName, col.DBColumnName)
		expected[idxName] = true

		// Uvek dropiraj i rekreira kao parcijalni indeks (WHERE is_deleted = FALSE)
		// da bi duplikati bili dozvoljeni za obrisane zapise.
		if existing[idxName] {
			if _, err := execMigration(db, fmt.Sprintf("DROP INDEX IF EXISTS %s", idxName)); err != nil {
				return fmt.Errorf("greška pri uklanjanju starog unique indeksa '%s': %w", idxName, err)
			}
		}

		query := fmt.Sprintf(
			"CREATE UNIQUE INDEX %s ON %s (%s) WHERE is_deleted = FALSE",
			idxName, mod.DBTableName, col.DBColumnName,
		)
		log.Printf("MIGRATION: Kreiram parcijalni unique indeks '%s' na '%s.%s'", idxName, mod.DBTableName, col.DBColumnName)
		if _, err := execMigration(db, query); err != nil {
			return fmt.Errorf("greška pri kreiranju unique indeksa '%s': %w", idxName, err)
		}
	}

	for idxName := range existing {
		if expected[idxName] {
			continue
		}
		log.Printf("MIGRATION: Uklanjam unique indeks '%s' jer više nije u definiciji modula '%s'", idxName, mod.ID)
		if _, err := execMigration(db, fmt.Sprintf("DROP INDEX IF EXISTS %s", idxName)); err != nil {
			return fmt.Errorf("greška pri uklanjanju unique indeksa '%s': %w", idxName, err)
		}
	}

	return nil
}

func perfIndexName(tableName, columnName string) string {
	return fmt.Sprintf("idx_%s_%s", sanitizeIdentifierPart(tableName), sanitizeIdentifierPart(columnName))
}

func getManagedPerfIndexes(db *sql.DB, tableName string) (map[string]bool, error) {
	rows, err := db.Query(`
		SELECT indexname
		FROM pg_indexes
		WHERE schemaname = 'public'
		  AND tablename = $1
		  AND indexname LIKE $2
	`, tableName, fmt.Sprintf("idx_%s_%%", sanitizeIdentifierPart(tableName)))
	if err != nil {
		return nil, fmt.Errorf("greška pri čitanju performansnih indeksa za tabelu '%s': %w", tableName, err)
	}
	defer rows.Close()

	idx := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		idx[name] = true
	}
	return idx, rows.Err()
}

func syncPerformanceIndexes(db *sql.DB, mod *ModuleDefinition) error {
	existing, err := getManagedPerfIndexes(db, mod.DBTableName)
	if err != nil {
		return err
	}

	expected := make(map[string]bool)

	// is_deleted uvek treba indeks jer filtriramo po njemu na svakom SELECT-u
	delIdxName := perfIndexName(mod.DBTableName, "is_deleted")
	expected[delIdxName] = true
	if !existing[delIdxName] {
		query := fmt.Sprintf("CREATE INDEX %s ON %s (is_deleted)", delIdxName, mod.DBTableName)
		log.Printf("MIGRATION: Kreiram indeks '%s'", delIdxName)
		if _, err := execMigration(db, query); err != nil {
			return fmt.Errorf("greška pri kreiranju indeksa '%s': %w", delIdxName, err)
		}
	}

	for _, col := range mod.Columns {
		if col.DBColumnName == "" || col.IsPrimaryKey {
			continue
		}

		needs := col.Type == "lookup" || col.IsSearchable || col.IsSortable
		if !needs {
			continue
		}

		idxName := perfIndexName(mod.DBTableName, col.DBColumnName)
		expected[idxName] = true
		if existing[idxName] {
			continue
		}

		query := fmt.Sprintf("CREATE INDEX %s ON %s (%s)", idxName, mod.DBTableName, col.DBColumnName)
		log.Printf("MIGRATION: Kreiram performansni indeks '%s' na '%s.%s'", idxName, mod.DBTableName, col.DBColumnName)
		if _, err := execMigration(db, query); err != nil {
			return fmt.Errorf("greška pri kreiranju indeksa '%s': %w", idxName, err)
		}
	}

	for idxName := range existing {
		if expected[idxName] {
			continue
		}
		log.Printf("MIGRATION: Uklanjam indeks '%s' jer više nije potreban", idxName)
		if _, err := execMigration(db, fmt.Sprintf("DROP INDEX IF EXISTS %s", idxName)); err != nil {
			return fmt.Errorf("greška pri uklanjanju indeksa '%s': %w", idxName, err)
		}
	}

	return nil
}

func getPrimaryKeyColumnForModule(mod *ModuleDefinition) *ColumnDefinition {
	for i := range mod.Columns {
		if mod.Columns[i].IsPrimaryKey {
			return &mod.Columns[i]
		}
	}
	return nil
}

func getManagedFKConstraints(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query(`
		SELECT c.conname, t.relname
		FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE c.contype = 'f'
		  AND c.conname LIKE 'fk_%'
		  AND n.nspname = 'public'
	`)
	if err != nil {
		return nil, fmt.Errorf("greška pri čitanju FK constraint-a: %w", err)
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var name, tableName string
		if err := rows.Scan(&name, &tableName); err != nil {
			return nil, err
		}
		out[name] = tableName
	}

	return out, rows.Err()
}

func syncForeignKeys(db *sql.DB, modules map[string]*ModuleDefinition) error {
	existing, err := getManagedFKConstraints(db)
	if err != nil {
		return err
	}

	expected := make(map[string]string)

	for _, mod := range modules {
		if !mod.IsTable || mod.DBTableName == "" {
			continue
		}

		for _, col := range mod.Columns {
			if col.Type != "lookup" || col.DBColumnName == "" || col.LookupModule == nil || col.LookupModule.DBTableName == "" {
				continue
			}

			targetPK := getPrimaryKeyColumnForModule(col.LookupModule)
			if targetPK == nil || targetPK.DBColumnName == "" {
				continue
			}

			name := fkConstraintName(mod.DBTableName, col.DBColumnName)
			expected[name] = fmt.Sprintf(
				"ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s(%s) ON UPDATE CASCADE ON DELETE RESTRICT",
				mod.DBTableName,
				name,
				col.DBColumnName,
				col.LookupModule.DBTableName,
				targetPK.DBColumnName,
			)
		}

		parentPK := getPrimaryKeyColumnForModule(mod)
		if parentPK == nil || parentPK.DBColumnName == "" {
			continue
		}

		for _, sub := range mod.SubModules {
			if sub.TargetModule == nil || sub.TargetModule.DBTableName == "" || sub.ChildForeignKeyField == "" {
				continue
			}

			name := fkConstraintName(sub.TargetModule.DBTableName, sub.ChildForeignKeyField)
			expected[name] = fmt.Sprintf(
				"ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s(%s) ON UPDATE CASCADE ON DELETE RESTRICT",
				sub.TargetModule.DBTableName,
				name,
				sub.ChildForeignKeyField,
				mod.DBTableName,
				parentPK.DBColumnName,
			)
		}
	}

	for name, ddl := range expected {
		log.Printf("MIGRATION: Obezbeđujem FK '%s'", name)
		if _, err := execMigration(db, ddl); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "already exists") {
				continue
			}
			return fmt.Errorf("greška pri kreiranju FK '%s': %w", name, err)
		}
	}

	for name, tableName := range existing {
		if _, keep := expected[name]; keep {
			continue
		}
		log.Printf("MIGRATION: Uklanjam FK '%s' jer više nije u definiciji modula", name)
		if _, err := execMigration(db, fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s", tableName, name)); err != nil {
			return fmt.Errorf("greška pri uklanjanju FK '%s': %w", name, err)
		}
	}

	return nil
}

// sqlTypesCompatible proverava da li su SQL tipovi kompatibilni
// (PostgreSQL može da vrati "integer", "text", "numeric" itd.)
func sqlTypesCompatible(dbType, expectedType string) bool {
	db := strings.ToUpper(dbType)
	exp := strings.ToUpper(expectedType)

	// Normalizacija sinonima
	synonyms := map[string]string{
		"INT4":                        "INTEGER",
		"INT8":                        "BIGINT",
		"INT":                         "INTEGER",
		"CHARACTER VARYING":           "TEXT",
		"CHARACTER":                   "TEXT",
		"VARCHAR":                     "TEXT",
		"DOUBLE PRECISION":            "NUMERIC",
		"FLOAT":                       "NUMERIC",
		"REAL":                        "NUMERIC",
		"TIMESTAMP WITHOUT TIME ZONE": "TIMESTAMP",
		"TIMESTAMP WITH TIME ZONE":    "TIMESTAMP",
	}

	if v, ok := synonyms[db]; ok {
		db = v
	}
	if v, ok := synonyms[exp]; ok {
		exp = v
	}

	return db == exp
}

// RunMigrations pokreće migracije za sve module tipa "table".
// Kreira nove tabele i dodaje nedostajuće kolone za promenjene definicije.
// ensureAdminUser osigurava da postoji admin korisnik
func ensureAdminUser(db *sql.DB) error {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM users WHERE username = 'admin'").Scan(&count)
	if err != nil {
		return fmt.Errorf("greška pri proveri admin korisnika: %w", err)
	}

	if count > 0 {
		return nil // Admin već postoji
	}

	passwordHash, err := hashPassword("admin")
	if err != nil {
		return fmt.Errorf("greška pri heširanju admin lozinke: %w", err)
	}

	log.Printf("MIGRATION: Kreiram admin korisnika sa inicijalnom lozinkom 'admin'")
	_, err = execMigration(db,
		"INSERT INTO users (username, password_hash, is_admin, created_at) VALUES ($1, $2, $3, NOW())",
		"admin", passwordHash, true,
	)
	if err != nil {
		return fmt.Errorf("greška pri kreiranju admin korisnika: %w", err)
	}

	return nil
}

// ensureDefaultRoles kreira default role sa dozvolama
func ensureDefaultRoles(db *sql.DB, appConfig *AppConfig) error {
	var roleID int64
	err := db.QueryRow("SELECT id FROM roles WHERE name = 'User'").Scan(&roleID)
	if err == sql.ErrNoRows {
		log.Printf("MIGRATION: Kreiram 'User' rolu")
		err = db.QueryRow(
			"INSERT INTO roles (name) VALUES ($1) RETURNING id",
			"User",
		).Scan(&roleID)
		if err != nil {
			return fmt.Errorf("greška pri kreiranju 'User' role: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("greška pri čitanju 'User' role: %w", err)
	}

	for _, mod := range appConfig.Modules {
		if !mod.IsPermissionResource() {
			continue
		}

		perms := mod.DefaultUserPermissions()
		_, err := execMigration(db,
			`INSERT INTO role_permissions (role_id, resource, permissions)
			 VALUES ($1, $2, $3)
			 ON CONFLICT (role_id, resource) DO UPDATE SET permissions = EXCLUDED.permissions`,
			roleID, mod.ID, perms,
		)
		if err != nil {
			return fmt.Errorf("greška pri postavljanju dozvola 'User' role za resurs '%s': %w", mod.ID, err)
		}
	}

	return nil
}

func syncResources(db *sql.DB, appConfig *AppConfig) error {
	for _, mod := range appConfig.Modules {
		if !mod.IsPermissionResource() {
			continue
		}

		requiredPermissions := mod.SupportedPermissions()
		_, err := execMigration(db,
			`INSERT INTO resources (resource, resource_type, required_permissions, updated_at)
			 VALUES ($1, $2, $3, NOW())
			 ON CONFLICT (resource) DO UPDATE
			 SET resource_type = EXCLUDED.resource_type,
			     required_permissions = EXCLUDED.required_permissions,
			     updated_at = NOW()`,
			mod.ID, mod.Type, requiredPermissions,
		)
		if err != nil {
			return fmt.Errorf("greška pri sinhronizaciji resursa '%s': %w", mod.ID, err)
		}
	}

	return nil
}

// ensureRBACTables kreira tabele za sistem dozvola ako ne postoje
func ensureRBACTables(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id SERIAL PRIMARY KEY,
			username VARCHAR(255) NOT NULL UNIQUE,
			password_hash VARCHAR(255) NOT NULL,
			is_admin BOOLEAN NOT NULL DEFAULT FALSE,
			created_at TIMESTAMP NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS roles (
			id SERIAL PRIMARY KEY,
			name VARCHAR(255) NOT NULL UNIQUE
		)`,
		`CREATE TABLE IF NOT EXISTS user_roles (
			user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			role_id INTEGER NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
			PRIMARY KEY (user_id, role_id)
		)`,
		`CREATE TABLE IF NOT EXISTS role_permissions (
			id SERIAL PRIMARY KEY,
			role_id INTEGER NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
			resource VARCHAR(255) NOT NULL,
			permissions BIGINT NOT NULL,
			UNIQUE (role_id, resource)
		)`,
		`CREATE TABLE IF NOT EXISTS resources (
			resource VARCHAR(255) PRIMARY KEY,
			resource_type VARCHAR(50) NOT NULL,
			required_permissions BIGINT NOT NULL DEFAULT 0,
			updated_at TIMESTAMP NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_user_roles_role_id ON user_roles(role_id)`,
		`CREATE INDEX IF NOT EXISTS idx_role_permissions_role_id ON role_permissions(role_id)`,
	}

	for _, stmt := range statements {
		log.Printf("MIGRATION: Izvršavam RBAC DDL: %s", stmt)
		if _, err := execMigration(db, stmt); err != nil {
			return fmt.Errorf("greška pri kreiranju RBAC tabele: %w", err)
		}
	}

	return nil
}

func ensureAuditLogTable(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS audit_log (
			id BIGSERIAL PRIMARY KEY,
			module_id VARCHAR(255) NOT NULL,
			record_id VARCHAR(255) NOT NULL,
			action VARCHAR(20) NOT NULL,
			actor_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
			actor_username VARCHAR(255),
			request_id VARCHAR(255),
			old_data JSONB,
			new_data JSONB,
			created_at TIMESTAMP NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_log_module_id ON audit_log(module_id)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_log_record_id ON audit_log(record_id)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_log_actor_user_id ON audit_log(actor_user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_log_created_at ON audit_log(created_at)`,
	}

	for _, stmt := range statements {
		if _, err := execMigration(db, stmt); err != nil {
			return fmt.Errorf("greška pri kreiranju audit log infrastrukture: %w", err)
		}
	}

	return nil
}

func (s *SQLDataset) RunMigrations() error {
	if isMigrationDryRun() {
		log.Println("MIGRATION DRY-RUN: Pokrećem plan migracija bez izvršenja SQL upisa.")
	}

	if err := ensureMigrationsTable(s.db); err != nil {
		return err
	}

	if err := ensureRBACTables(s.db); err != nil {
		return err
	}

	if err := ensureAuditLogTable(s.db); err != nil {
		return err
	}

	if err := syncResources(s.db, s.config); err != nil {
		return err
	}

	for _, mod := range s.config.Modules {
		if !mod.IsTable || mod.DBTableName == "" {
			continue
		}

		currentHash, err := columnsHash(mod.Columns)
		if err != nil {
			log.Printf("MIGRATION ERROR: Ne mogu da izračunam hash za modul '%s': %v", mod.ID, err)
			continue
		}

		storedHash, err := getStoredHash(s.db, mod.ID)
		if err != nil {
			log.Printf("MIGRATION ERROR: Ne mogu da dobijem sačuvani hash za modul '%s': %v", mod.ID, err)
			continue
		}

		// Uvek proveri kolone tabele (potrebno za audit/soft-delete proveru)
		existingCols, err := getExistingDBColumns(s.db, mod.DBTableName)
		if err != nil {
			log.Printf("MIGRATION ERROR: Ne mogu da pročitam kolone tabele '%s': %v", mod.DBTableName, err)
			continue
		}

		if len(existingCols) == 0 {
			// Tabela ne postoji — kreiraj je
			if err := createTable(s.db, mod); err != nil {
				log.Printf("MIGRATION ERROR: %v", err)
				continue
			}
			existingCols = map[string]string{"is_deleted": "BOOLEAN", "created_at": "TIMESTAMP", "updated_at": "TIMESTAMP"}
		}

		// Uvek dodaj managed kolone ako nedostaju (bez obzira na hash)
		if err := ensureSoftDeleteColumn(s.db, mod.DBTableName, existingCols); err != nil {
			log.Printf("MIGRATION ERROR: %v", err)
			continue
		}

		if err := ensureAuditColumns(s.db, mod.DBTableName, existingCols); err != nil {
			log.Printf("MIGRATION ERROR: %v", err)
			continue
		}

		if storedHash == currentHash {
			log.Printf("MIGRATION: Modul '%s' — bez promena u shemi, preskačem.", mod.ID)
			continue
		}

		// Hash se promenio — primeni izmene iz JSON definicije
		log.Printf("MIGRATION: Detektovana promena u definiciji modula '%s', ažuriram tabelu '%s'...", mod.ID, mod.DBTableName)
		if err := alterTable(s.db, mod, existingCols); err != nil {
			log.Printf("MIGRATION ERROR: %v", err)
			continue
		}

		if err := syncUniqueIndexes(s.db, mod); err != nil {
			log.Printf("MIGRATION ERROR: %v", err)
			continue
		}

		if err := syncPerformanceIndexes(s.db, mod); err != nil {
			log.Printf("MIGRATION ERROR: %v", err)
			continue
		}

		// Sačuvaj novi hash
		if err := upsertMigrationRecord(s.db, mod.ID, mod.DBTableName, currentHash); err != nil {
			log.Printf("MIGRATION ERROR: %v", err)
		}
	}

	if err := syncForeignKeys(s.db, s.config.Modules); err != nil {
		return err
	}

	if err := ensureAdminUser(s.db); err != nil {
		return err
	}

	if err := ensureDefaultRoles(s.db, s.config); err != nil {
		return err
	}

	if isMigrationDryRun() {
		log.Println("MIGRATION DRY-RUN: Plan migracija završen.")
		return nil
	}

	log.Println("MIGRATION: Migracije završene.")
	return nil
}
