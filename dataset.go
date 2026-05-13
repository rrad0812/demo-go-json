// dataset.go
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq" // PostgreSQL drajver
)

// Definisanje greške za jedinstvenu vrednost
var ErrUniqueViolation = errors.New("unique violation")

func mapWriteError(moduleDef *ModuleDefinition, err error) error {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && string(pqErr.Code) == "23505" {
		return fmt.Errorf("jedinstvena vrednost već postoji u modulu '%s': %w", moduleDef.Name, ErrUniqueViolation)
	}
	return err
}

func includeDeleted(queryParams url.Values) bool {
	v := queryParams.Get("_include_deleted")
	if strings.TrimSpace(v) == "" {
		v = queryParams.Get("includeDeleted")
	}
	if strings.TrimSpace(v) == "" {
		v = queryParams.Get("include_deleted")
	}
	v = strings.TrimSpace(strings.ToLower(v))
	return v == "1" || v == "true" || v == "yes"
}

func parseNonNegativeIntParam(queryParams url.Values, keys ...string) (int, bool) {
	for _, key := range keys {
		value := strings.TrimSpace(queryParams.Get(key))
		if value == "" {
			continue
		}

		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			log.Printf("WARNING: Nevažeća vrednost za %s: '%s'", key, value)
			return 0, false
		}

		return parsed, true
	}

	return 0, false
}

func normalizeSortDirection(raw string) (string, bool) {
	direction := strings.ToUpper(strings.TrimSpace(raw))
	if direction == "" {
		return "ASC", true
	}

	if direction != "ASC" && direction != "DESC" {
		return "", false
	}

	return direction, true
}

func buildWhitelistedOrderBy(moduleDef *ModuleDefinition, sortBy, sortDir string) (string, bool) {
	columnName := strings.TrimSpace(sortBy)
	if columnName == "" {
		return "", false
	}

	colDef := getColumnByDBName(moduleDef.Columns, columnName)
	if colDef == nil {
		log.Printf("WARNING: Pokušaj sortiranja po nepostojećoj koloni: '%s'", columnName)
		return "", false
	}

	direction, ok := normalizeSortDirection(sortDir)
	if !ok {
		log.Printf("WARNING: Nevažeća vrednost za sort_dir: '%s'", sortDir)
		return "", false
	}

	return fmt.Sprintf("%s %s", colDef.DBColumnName, direction), true
}

// SQLDataset handles database operations.
type SQLDataset struct {
	db          *sql.DB
	config      *AppConfig // Dodato za pristup AppConfig i GetModuleByID
	auditEnable bool
}

type AuditAction string

const (
	AuditActionCreate AuditAction = "create"
	AuditActionUpdate AuditAction = "update"
	AuditActionDelete AuditAction = "delete"
)

type AuditEvent struct {
	ModuleID      string
	RecordID      string
	Action        AuditAction
	ActorUserID   int64
	ActorUsername string
	RequestID     string
	OldData       interface{}
	NewData       interface{}
}

type AuditQueryOptions struct {
	ModuleID    string
	RecordID    string
	ActorUserID *int64
	ActorName   string
	Action      string
	FromTime    string
	ToTime      string
	Limit       int
	Offset      int
	SortBy      string
	SortDir     string
}

// NewSQLDataset creates a new SQLDataset instance.
func NewSQLDataset(config *AppConfig) (*SQLDataset, error) {
	connStr := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		config.GetDatabaseConfig().Host,
		config.GetDatabaseConfig().Port,
		config.GetDatabaseConfig().User,
		config.GetDatabaseConfig().Password,
		config.GetDatabaseConfig().DBName,
		config.GetDatabaseConfig().SSLMode,
	)

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, fmt.Errorf("greška pri otvaranju baze podataka: %w", err)
	}

	if err = db.Ping(); err != nil {
		return nil, fmt.Errorf("greška pri povezivanju sa bazom podataka: %w", err)
	}

	log.Println("INFO: Uspešno povezano sa bazom podataka.")
	return &SQLDataset{db: db, config: config, auditEnable: true}, nil
}

func (s *SQLDataset) LogAuditEvent(evt AuditEvent) error {
	if !s.auditEnable {
		return nil
	}

	if strings.TrimSpace(evt.ModuleID) == "" || strings.TrimSpace(evt.RecordID) == "" {
		return nil
	}

	if evt.Action != AuditActionCreate && evt.Action != AuditActionUpdate && evt.Action != AuditActionDelete {
		return fmt.Errorf("nepodržana audit akcija: %s", evt.Action)
	}

	var oldJSON interface{}
	if evt.OldData != nil {
		encoded, err := json.Marshal(evt.OldData)
		if err != nil {
			return fmt.Errorf("greška pri serijalizaciji old_data: %w", err)
		}
		oldJSON = encoded
	}

	var newJSON interface{}
	if evt.NewData != nil {
		encoded, err := json.Marshal(evt.NewData)
		if err != nil {
			return fmt.Errorf("greška pri serijalizaciji new_data: %w", err)
		}
		newJSON = encoded
	}

	_, err := s.db.Exec(`
		INSERT INTO audit_log (
			module_id, record_id, action,
			actor_user_id, actor_username, request_id,
			old_data, new_data, created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8::jsonb, NOW())
	`,
		evt.ModuleID,
		evt.RecordID,
		string(evt.Action),
		evt.ActorUserID,
		evt.ActorUsername,
		evt.RequestID,
		oldJSON,
		newJSON,
	)
	if err != nil {
		return fmt.Errorf("greška pri upisu audit loga: %w", err)
	}

	return nil
}

func (s *SQLDataset) GetAuditLogs(opts AuditQueryOptions) ([]map[string]interface{}, error) {
	baseQuery := `
		SELECT id, module_id, record_id, action,
		       actor_user_id, actor_username, request_id,
		       old_data, new_data, created_at
		FROM audit_log
	`

	whereClauses := []string{}
	args := []interface{}{}
	argCounter := 1

	if strings.TrimSpace(opts.ModuleID) != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("module_id = $%d", argCounter))
		args = append(args, strings.TrimSpace(opts.ModuleID))
		argCounter++
	}

	if strings.TrimSpace(opts.RecordID) != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("record_id = $%d", argCounter))
		args = append(args, strings.TrimSpace(opts.RecordID))
		argCounter++
	}

	if opts.ActorUserID != nil {
		whereClauses = append(whereClauses, fmt.Sprintf("actor_user_id = $%d", argCounter))
		args = append(args, *opts.ActorUserID)
		argCounter++
	}

	if strings.TrimSpace(opts.ActorName) != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("actor_username ILIKE $%d", argCounter))
		args = append(args, "%"+strings.TrimSpace(opts.ActorName)+"%")
		argCounter++
	}

	if strings.TrimSpace(opts.Action) != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("action = $%d", argCounter))
		args = append(args, strings.TrimSpace(opts.Action))
		argCounter++
	}

	if strings.TrimSpace(opts.FromTime) != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("created_at >= $%d", argCounter))
		args = append(args, strings.TrimSpace(opts.FromTime))
		argCounter++
	}

	if strings.TrimSpace(opts.ToTime) != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("created_at <= $%d", argCounter))
		args = append(args, strings.TrimSpace(opts.ToTime))
		argCounter++
	}

	finalQuery := baseQuery
	if len(whereClauses) > 0 {
		finalQuery += " WHERE " + strings.Join(whereClauses, " AND ")
	}

	allowedSortColumns := map[string]string{
		"id":            "id",
		"module_id":     "module_id",
		"record_id":     "record_id",
		"action":        "action",
		"actor_user_id": "actor_user_id",
		"created_at":    "created_at",
	}

	sortColumn := "created_at"
	if requested := strings.TrimSpace(opts.SortBy); requested != "" {
		if mapped, ok := allowedSortColumns[requested]; ok {
			sortColumn = mapped
		}
	}

	sortDir := "DESC"
	if strings.TrimSpace(opts.SortDir) != "" {
		if parsedDir, ok := normalizeSortDirection(opts.SortDir); ok {
			sortDir = parsedDir
		}
	}

	finalQuery += fmt.Sprintf(" ORDER BY %s %s", sortColumn, sortDir)

	if opts.Limit > 0 {
		finalQuery += fmt.Sprintf(" LIMIT $%d", argCounter)
		args = append(args, opts.Limit)
		argCounter++
	}

	if opts.Offset > 0 {
		finalQuery += fmt.Sprintf(" OFFSET $%d", argCounter)
		args = append(args, opts.Offset)
		argCounter++
	}

	rows, err := s.db.Query(finalQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("greška pri dohvatanju audit logova: %w", err)
	}
	defer rows.Close()

	columnNames, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("greška pri dohvatanju imena audit kolona: %w", err)
	}

	results := make([]map[string]interface{}, 0)
	for rows.Next() {
		columns := make([]interface{}, len(columnNames))
		pointers := make([]interface{}, len(columnNames))
		for i := range columns {
			pointers[i] = &columns[i]
		}

		if err := rows.Scan(pointers...); err != nil {
			return nil, fmt.Errorf("greška pri skeniranju audit reda: %w", err)
		}

		rec := make(map[string]interface{})
		for i, colName := range columnNames {
			val := columns[i]
			if b, ok := val.([]byte); ok {
				rec[colName] = string(b)
				continue
			}
			rec[colName] = val
		}

		results = append(results, rec)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("greška nakon iteracije audit redova: %w", err)
	}

	return results, nil
}

// Close closes the database connection.
func (s *SQLDataset) Close() {
	if s.db != nil {
		s.db.Close()
		log.Println("INFO: Veza sa bazom podataka zatvorena.")
	}
}

func (s *SQLDataset) ReadyCheck() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("database handle is nil")
	}

	var one int
	if err := s.db.QueryRow("SELECT 1").Scan(&one); err != nil {
		return err
	}

	if one != 1 {
		return fmt.Errorf("unexpected ready check result: %d", one)
	}

	return nil
}

// GetRecords fetches records for a given module, applying filters, sorting, and pagination.
func (s *SQLDataset) GetRecords(moduleDef *ModuleDefinition, queryParams url.Values) ([]map[string]interface{}, error) {
	if moduleDef.DBTableName == "" && moduleDef.SelectQuery == "" {
		return nil, fmt.Errorf("modul '%s' nema definisanu tabelu ili select query", moduleDef.ID)
	}

	var baseQuery string
	if moduleDef.SelectQuery != "" {
		baseQuery = moduleDef.SelectQuery
	} else {
		// Konstruiši SELECT * FROM table_name ako SelectQuery nije definisan
		baseQuery = fmt.Sprintf("SELECT * FROM %s", moduleDef.DBTableName)
	}

	// Liste za SQL WHERE klauzulu i argumente za prepared statement
	whereClauses := []string{}
	args := []interface{}{}
	argCounter := 1 // Brojač za parametre ($1, $2, ...)

	// Limit i Offset
	limit := -1  // -1 znači bez limita
	offset := -1 // -1 znači bez offseta
	if parsedLimit, ok := parseNonNegativeIntParam(queryParams, "_limit", "limit"); ok {
		limit = parsedLimit
	}
	if parsedOffset, ok := parseNonNegativeIntParam(queryParams, "_offset", "offset"); ok {
		offset = parsedOffset
	}

	// Sortiranje
	orderByClauses := []string{}

	// Prođi kroz query parametre
	for key, values := range queryParams {
		if len(values) == 0 {
			continue
		}
		value := values[0] // Uzimamo samo prvu vrednost za svaki parametar

		switch key {
		case "_limit", "limit", "_offset", "offset":
			continue
		case "_sort":
			// Primer: _sort=column1,-column2
			sortFields := strings.Split(value, ",")
			for _, field := range sortFields {
				field = strings.TrimSpace(field)
				if field == "" {
					continue
				}
				order := "ASC"
				columnName := field
				if strings.HasPrefix(field, "-") {
					order = "DESC"
					columnName = strings.TrimPrefix(field, "-")
				}
				// Proveri da li je kolona validna (da sprečimo SQL injection)
				if colDef := getColumnByDBName(moduleDef.Columns, columnName); colDef != nil {
					orderByClauses = append(orderByClauses, fmt.Sprintf("%s %s", colDef.DBColumnName, order))
				} else {
					log.Printf("WARNING: Pokušaj sortiranja po nepostojećoj koloni: '%s'", columnName)
				}
			}
		case "_search":
			// Pozovi pomoćnu funkciju za pretragu
			s.addSearchCondition(moduleDef, value, &whereClauses, &args, &argCounter)
		case "sort_by", "sortBy", "sort_dir", "sortDir":
			// Obrađuje se nakon petlje sa whitelist validacijom.
			continue
		case "_include_deleted", "includeDeleted", "include_deleted":
			// Kontrolni parametar za soft-delete filter se obrađuje niže.
			continue
		default:
			// Standardno filtriranje po kolonama (npr. 'column=value' ili 'column__gt=value')
			s.buildWhereClause(moduleDef, key, value, &whereClauses, &args, &argCounter)
		}
	}

	if len(orderByClauses) == 0 {
		sortBy := queryParams.Get("sort_by")
		if strings.TrimSpace(sortBy) == "" {
			sortBy = queryParams.Get("sortBy")
		}
		sortDir := queryParams.Get("sort_dir")
		if strings.TrimSpace(sortDir) == "" {
			sortDir = queryParams.Get("sortDir")
		}

		if clause, ok := buildWhitelistedOrderBy(moduleDef, sortBy, sortDir); ok {
			orderByClauses = append(orderByClauses, clause)
		}
	}

	// Izgradnja finalnog SQL upita
	finalQuery := baseQuery

	if moduleDef.SelectQuery == "" && !includeDeleted(queryParams) {
		whereClauses = append(whereClauses, "is_deleted = FALSE")
	}

	if len(whereClauses) > 0 {
		finalQuery += " WHERE " + strings.Join(whereClauses, " AND ")
	}
	if len(orderByClauses) > 0 {
		finalQuery += " ORDER BY " + strings.Join(orderByClauses, ", ")
	}
	if limit != -1 {
		finalQuery += fmt.Sprintf(" LIMIT $%d", argCounter)
		args = append(args, limit)
		argCounter++
	}
	if offset != -1 {
		finalQuery += fmt.Sprintf(" OFFSET $%d", argCounter)
		args = append(args, offset)
		argCounter++
	}

	log.Printf("INFO: Izvršavanje SQL upita: %s sa parametrima: %v", finalQuery, args)

	rows, err := s.db.Query(finalQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("greška pri izvršavanju SELECT upita za modul '%s': %w", moduleDef.ID, err)
	}
	defer rows.Close()

	records := make([]map[string]interface{}, 0)
	columnNames, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("greška pri dohvatanju imena kolona: %w", err)
	}

	for rows.Next() {
		columns := make([]interface{}, len(columnNames))
		columnPointers := make([]interface{}, len(columnNames))
		for i := range columns {
			columnPointers[i] = &columns[i]
		}

		if err := rows.Scan(columnPointers...); err != nil {
			return nil, fmt.Errorf("greška pri skeniranju reda: %w", err)
		}

		record := make(map[string]interface{})
		for i, colName := range columnNames {
			val := columns[i]
			// dbColDef := getColumnByDBName(moduleDef.Columns, colName) // Možda ti treba za tip
			if val == nil {
				record[colName] = nil
			} else {
				// PostgreSQL vraća neke tipove kao []byte, konvertujemo ih u string ako je to očekivano
				switch v := val.(type) {
				case []byte:
					record[colName] = string(v)
				default:
					record[colName] = v
				}
			}
		}

		records = append(records, record)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("greška nakon iteracije kroz redove: %w", err)
	}

	// Proširenje lookup i submodule polja
	if err := s.performLookupExpansion(records, moduleDef); err != nil {
		log.Printf("WARNING: Greška pri proširenju lookup-a za modul '%s': %v", moduleDef.ID, err)
		// Opcionalno: vrati grešku ili samo nastavi bez proširenja
	}

	if len(moduleDef.SubModules) > 0 {
		if pkCol := s.getPrimaryKeyColumn(moduleDef); pkCol != nil {
			if err := s.performSubmoduleExpansionBatch(records, moduleDef, pkCol.DBColumnName); err != nil {
				log.Printf("WARNING: Greška pri batch proširenju submodula za modul '%s': %v", moduleDef.ID, err)
			}
		} else {
			log.Printf("WARNING: Modul '%s' ima submodule ali nema definisan primarni ključ za proširenje.", moduleDef.ID)
		}
	}

	return records, nil
}

// GetReportData executes a select_query for report or custom type modules.
func (s *SQLDataset) GetReportData(moduleDef *ModuleDefinition, queryParams url.Values) ([]map[string]interface{}, error) {
	if moduleDef.SelectQuery == "" {
		return nil, fmt.Errorf("modul '%s' tipa '%s' nema definisan select_query", moduleDef.Name, moduleDef.Type)
	}

	query := moduleDef.SelectQuery
	args := []interface{}{}
	argCounter := 1

	sortBy := queryParams.Get("sort_by")
	if strings.TrimSpace(sortBy) == "" {
		sortBy = queryParams.Get("sortBy")
	}
	sortDir := queryParams.Get("sort_dir")
	if strings.TrimSpace(sortDir) == "" {
		sortDir = queryParams.Get("sortDir")
	}
	if strings.TrimSpace(sortDir) == "" {
		sortDir = queryParams.Get("sortOrder")
	}

	if clause, ok := buildWhitelistedOrderBy(moduleDef, sortBy, sortDir); ok {
		query += " ORDER BY " + clause
	}

	if limit, ok := parseNonNegativeIntParam(queryParams, "_limit", "limit"); ok {
		query += fmt.Sprintf(" LIMIT $%d", argCounter)
		args = append(args, limit)
		argCounter++
	}
	if offset, ok := parseNonNegativeIntParam(queryParams, "_offset", "offset"); ok {
		query += fmt.Sprintf(" OFFSET $%d", argCounter)
		args = append(args, offset)
		argCounter++
	}

	log.Printf("DEBUG: Executing report query for '%s': %s args=%v", moduleDef.ID, query, args)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("greška pri izvršavanju REPORT upita za modul '%s': %w", moduleDef.Name, err)
	}
	defer rows.Close()

	columnNames, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("greška pri dohvatanju imena kolona za izveštaj '%s': %w", moduleDef.Name, err)
	}

	var results []map[string]interface{}
	for rows.Next() {
		record := make(map[string]interface{})
		columnPointers := make([]interface{}, len(columnNames))
		columnValues := make([]interface{}, len(columnNames))

		for i := range columnNames {
			columnPointers[i] = &columnValues[i]
		}

		if err := rows.Scan(columnPointers...); err != nil {
			return nil, fmt.Errorf("greška pri skeniranju reda izveštaja za modul '%s': %w", moduleDef.Name, err)
		}

		for i, colName := range columnNames {
			val := columnValues[i]
			if val == nil {
				record[colName] = nil
			} else {
				switch v := val.(type) {
				case []byte:
					record[colName] = string(v)
				default:
					record[colName] = v
				}
			}
		}
		results = append(results, record)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("greška nakon iteracije kroz redove izveštaja za modul '%s': %w", moduleDef.Name, err)
	}

	return results, nil
}

// getColumnByDBName je pomoćna funkcija za pronalaženje definicije kolone po DBColumnName
func getColumnByDBName(columns []ColumnDefinition, dbColumnName string) *ColumnDefinition {
	for i := range columns {
		if columns[i].DBColumnName == dbColumnName {
			return &columns[i]
		}
	}
	return nil
}

// buildWhereClause parsira filter parametre i dodaje ih u WHERE klauzulu.
func (s *SQLDataset) buildWhereClause(moduleDef *ModuleDefinition, key, value string, whereClauses *[]string, args *[]interface{}, argCounter *int) {
	parts := strings.Split(key, "__")
	columnName := parts[0]
	operator := ""
	if len(parts) > 1 {
		operator = parts[1]
	}

	colDef := getColumnByDBName(moduleDef.Columns, columnName)
	if colDef == nil {
		log.Printf("WARNING: Pokušaj filtriranja po nepostojećoj koloni: '%s'", columnName)
		return
	}

	sqlOperator := "="
	needsValueConversion := true
	needsLikeEscape := false

	switch operator {
	case "gt":
		sqlOperator = ">"
	case "gte":
		sqlOperator = ">="
	case "lt":
		sqlOperator = "<"
	case "lte":
		sqlOperator = "<="
	case "ne":
		sqlOperator = "!="
	case "like":
		sqlOperator = "LIKE"
		needsLikeEscape = true
	case "ilike":
		sqlOperator = "ILIKE" // Case-insensitive LIKE za PostgreSQL
		needsLikeEscape = true
	case "in":
		// Poseban tretman za IN operator: vrednosti se splituju i dodaju kao zasebni parametri
		vals := strings.Split(value, ",")
		placeholders := make([]string, len(vals))
		for i, v := range vals {
			placeholders[i] = fmt.Sprintf("$%d", *argCounter)
			convertedVal, err := convertValueToColumnType(v, colDef.Type)
			if err != nil {
				log.Printf("WARNING: Greška pri konverziji IN vrednosti za kolonu '%s': %v", colDef.Name, err)
				return
			}
			*args = append(*args, convertedVal)
			*argCounter++
		}
		*whereClauses = append(*whereClauses, fmt.Sprintf("%s %s (%s)", colDef.DBColumnName, sqlOperator, strings.Join(placeholders, ", ")))
		needsValueConversion = false // Vrednosti su već konvertovane
	default:
		// Ako operator nije eksplicitno naveden, pretpostavljamo '='
		sqlOperator = "="
	}

	if needsValueConversion {
		convertedVal, err := convertValueToColumnType(value, colDef.Type)
		if err != nil {
			log.Printf("WARNING: Greška pri konverziji vrednosti '%s' za kolonu '%s' (%s): %v", value, colDef.Name, colDef.Type, err)
			return
		}
		if needsLikeEscape {
			convertedVal = fmt.Sprintf("%%%s%%", convertedVal) // Dodaj % za LIKE/ILIKE pretragu po podstringu
		}
		*whereClauses = append(*whereClauses, fmt.Sprintf("%s %s $%d", colDef.DBColumnName, sqlOperator, *argCounter))
		*args = append(*args, convertedVal)
		*argCounter++
	}
}

// addSearchCondition dodaje uslov pretrage za "_search" parametar.
func (s *SQLDataset) addSearchCondition(moduleDef *ModuleDefinition, searchValue string, whereClauses *[]string, args *[]interface{}, argCounter *int) {
	searchableColumns := []string{}
	for _, colDef := range moduleDef.Columns {
		// Pretpostavljamo da su sve string kolone (koje su visible i editable) pretražive
		// Možeš dodati i novo polje "IsSearchable" u ColumnDefinition ako želiš veću kontrolu
		if colDef.Type == "string" && colDef.IsVisible { // i colDef.IsSearchable ako dodas
			searchableColumns = append(searchableColumns, colDef.DBColumnName)
		}
	}

	if len(searchableColumns) == 0 {
		log.Printf("WARNING: Modul '%s' nema definisane pretražive kolone za _search.", moduleDef.ID)
		return
	}

	searchParts := make([]string, len(searchableColumns))
	for i, colName := range searchableColumns {
		searchParts[i] = fmt.Sprintf("%s ILIKE $%d", colName, *argCounter)
	}

	*whereClauses = append(*whereClauses, fmt.Sprintf("(%s)", strings.Join(searchParts, " OR ")))
	*args = append(*args, fmt.Sprintf("%%%s%%", searchValue)) // Pretraga po podstringu
	*argCounter++
}

// convertValueToColumnType pokušava da konvertuje string vrednost u odgovarajući tip kolone.
func convertValueToColumnType(value string, colType string) (interface{}, error) {
	switch colType {
	case "integer":
		return strconv.Atoi(value)
	case "float":
		return strconv.ParseFloat(value, 64)
	case "boolean":
		return strconv.ParseBool(value)
	case "string", "text":
		return value, nil
	// Dodaj i druge tipove ako su ti potrebni (npr. "date", "datetime")
	default:
		return value, nil // Za nepoznate tipove, vrati string
	}
}

// getPrimaryKeyColumn vraća definiciju primarnog ključa za modul.
// (Ova funkcija je verovatno već u dataset.go ili models.go, ali je ostavljam ovde za kontekst)
func (s *SQLDataset) getPrimaryKeyColumn(moduleDef *ModuleDefinition) *ColumnDefinition {
	for _, col := range moduleDef.Columns {
		if col.IsPrimaryKey {
			return &col
		}
	}
	return nil
}

// CreateRecord inserts a new record into the database.
// Vraća (interface{}, error) jer vraća ID novog zapisa.
func (s *SQLDataset) CreateRecord(moduleDef *ModuleDefinition, payload map[string]interface{}) (interface{}, error) {
	return s.createRecordWithForcedFields(moduleDef, payload, nil)
}

func (s *SQLDataset) CreateRecordWithForcedFields(moduleDef *ModuleDefinition, payload map[string]interface{}, forcedFields map[string]interface{}) (interface{}, error) {
	return s.createRecordWithForcedFields(moduleDef, payload, forcedFields)
}

func (s *SQLDataset) createRecordWithForcedFields(moduleDef *ModuleDefinition, payload map[string]interface{}, forcedFields map[string]interface{}) (interface{}, error) {
	if !moduleDef.IsTable {
		return nil, fmt.Errorf("kreiranje zapisa nije podržano za modul tipa '%s'", moduleDef.Type)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("greška pri pokretanju transakcije za kreiranje u modulu '%s': %w", moduleDef.Name, err)
	}
	defer tx.Rollback()

	cols := []string{}
	vals := []interface{}{}
	placeholders := []string{}

	now := time.Now()

	i := 1
	for _, colDef := range moduleDef.Columns {
		// Preskoči kolone koje nisu editable, primarne ključeve i read-only
		if colDef.IsPrimaryKey || colDef.IsReadOnly {
			continue
		}
		if val, ok := forcedFields[colDef.DBColumnName]; ok {
			cols = append(cols, colDef.DBColumnName)
			vals = append(vals, val)
			placeholders = append(placeholders, fmt.Sprintf("$%d", i))
			i++
			continue
		}
		if !colDef.IsEditable {
			continue
		}
		if val, ok := payload[colDef.DBColumnName]; ok {
			cols = append(cols, colDef.DBColumnName)
			vals = append(vals, val)
			placeholders = append(placeholders, fmt.Sprintf("$%d", i))
			i++
		} else if colDef.DefaultValue != nil {
			cols = append(cols, colDef.DBColumnName)
			vals = append(vals, colDef.DefaultValue)
			placeholders = append(placeholders, fmt.Sprintf("$%d", i))
			i++
		}
	}

	// Audit kolone
	cols = append(cols, "created_at", "updated_at")
	vals = append(vals, now, now)
	placeholders = append(placeholders, fmt.Sprintf("$%d", i), fmt.Sprintf("$%d", i+1))
	i += 2

	if len(cols) == 2 { // samo audit kolone, nema korisnih polja
		return nil, fmt.Errorf("nema validnih polja za kreiranje zapisa u modulu '%s'", moduleDef.Name)
	}

	pkCol := s.getPrimaryKeyColumn(moduleDef)
	if pkCol == nil {
		// ISPRAVLJENO: Vraća (nil, error)
		return nil, fmt.Errorf("modul '%s' nema definisan primarni ključ za povratak ID-a", moduleDef.Name)
	}

	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) RETURNING %s",
		moduleDef.DBTableName,
		strings.Join(cols, ", "),
		strings.Join(placeholders, ", "),
		pkCol.DBColumnName,
	)

	log.Printf("DEBUG: Executing INSERT query: %s with values: %v", query, vals)

	var newID interface{}
	err = tx.QueryRow(query, vals...).Scan(&newID)
	if err != nil {
		// ISPRAVLJENO: Vraća (nil, error)
		return nil, fmt.Errorf("greška pri izvršavanju INSERT upita za modul '%s': %w", moduleDef.Name, mapWriteError(moduleDef, err))
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("greška pri potvrdi transakcije za kreiranje u modulu '%s': %w", moduleDef.Name, err)
	}

	return newID, nil
}

// UpdateRecord updates an existing record in the database.
// Vraća samo error.
func (s *SQLDataset) UpdateRecord(moduleDef *ModuleDefinition, recordID string, payload map[string]interface{}) error {
	return s.updateRecordWithForcedFields(moduleDef, recordID, payload, nil)
}

func (s *SQLDataset) UpdateRecordWithForcedFields(moduleDef *ModuleDefinition, recordID string, payload map[string]interface{}, forcedFields map[string]interface{}) error {
	return s.updateRecordWithForcedFields(moduleDef, recordID, payload, forcedFields)
}

func (s *SQLDataset) updateRecordWithForcedFields(moduleDef *ModuleDefinition, recordID string, payload map[string]interface{}, forcedFields map[string]interface{}) error {
	if !moduleDef.IsTable {
		return fmt.Errorf("ažuriranje zapisa nije podržano za modul tipa '%s'", moduleDef.Type)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("greška pri pokretanju transakcije za ažuriranje u modulu '%s': %w", moduleDef.Name, err)
	}
	defer tx.Rollback()

	setClauses := []string{}
	vals := []interface{}{}
	i := 1

	pkCol := s.getPrimaryKeyColumn(moduleDef)
	if pkCol == nil {
		// ISPRAVLJENO: Vraća samo error
		return fmt.Errorf("modul '%s' nema definisan primarni ključ za ažuriranje", moduleDef.Name)
	}

	for _, colDef := range moduleDef.Columns {
		if colDef.IsPrimaryKey || colDef.IsReadOnly {
			continue
		}
		if val, ok := forcedFields[colDef.DBColumnName]; ok {
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", colDef.DBColumnName, i))
			vals = append(vals, val)
			i++
			continue
		}
		if !colDef.IsEditable {
			continue
		}
		if val, ok := payload[colDef.DBColumnName]; ok {
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", colDef.DBColumnName, i))
			vals = append(vals, val)
			i++
		}
	}

	// Audit: uvek ažuriraj updated_at
	setClauses = append(setClauses, fmt.Sprintf("updated_at = $%d", i))
	vals = append(vals, time.Now())
	i++

	if len(setClauses) == 1 {
		// samo updated_at, nema korisnih polja
		return fmt.Errorf("nema validnih polja za ažuriranje zapisa u modulu '%s'", moduleDef.Name)
	}

	// Dodaj recordID kao poslednji argument za WHERE klauzulu
	vals = append(vals, recordID)

	query := fmt.Sprintf("UPDATE %s SET %s WHERE %s = $%d",
		moduleDef.DBTableName,
		strings.Join(setClauses, ", "),
		pkCol.DBColumnName, i, // i je sada indeks poslednjeg placeholder-a ($d)
	)

	log.Printf("DEBUG: Executing UPDATE query: %s with values: %v", query, vals)

	res, err := tx.Exec(query, vals...)
	if err != nil {
		// ISPRAVLJENO: Vraća samo error
		return fmt.Errorf("greška pri izvršavanju UPDATE upita za modul '%s', ID '%s': %w", moduleDef.Name, recordID, mapWriteError(moduleDef, err))
	}

	rowsAffected, _ := res.RowsAffected()
	if rowsAffected == 0 {
		// ISPRAVLJENO: Vraća samo error
		return fmt.Errorf("zapis sa ID '%s' nije pronađen ili ažuriran u modulu '%s'", recordID, moduleDef.Name)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("greška pri potvrdi transakcije za ažuriranje u modulu '%s': %w", moduleDef.Name, err)
	}

	return nil
}

// DeleteRecord deletes a record from the database.
// Vraća samo error.
func (s *SQLDataset) DeleteRecord(moduleDef *ModuleDefinition, recordID string) error {
	if !moduleDef.IsTable {
		return fmt.Errorf("brisanje zapisa nije podržano za modul tipa '%s'", moduleDef.Type)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("greška pri pokretanju transakcije za brisanje u modulu '%s': %w", moduleDef.Name, err)
	}
	defer tx.Rollback()

	pkCol := s.getPrimaryKeyColumn(moduleDef)
	if pkCol == nil {
		// ISPRAVLJENO: Vraća samo error
		return fmt.Errorf("modul '%s' nema definisan primarni ključ za brisanje", moduleDef.Name)
	}

	query := fmt.Sprintf("UPDATE %s SET is_deleted = TRUE WHERE %s = $1 AND is_deleted = FALSE",
		moduleDef.DBTableName,
		pkCol.DBColumnName,
	)

	log.Printf("DEBUG: Executing soft DELETE query: %s with ID: %s", query, recordID)

	res, err := tx.Exec(query, recordID)
	if err != nil {
		// ISPRAVLJENO: Vraća samo error
		return fmt.Errorf("greška pri izvršavanju DELETE upita za modul '%s', ID '%s': %w", moduleDef.Name, recordID, err)
	}

	rowsAffected, _ := res.RowsAffected()
	if rowsAffected == 0 {
		// ISPRAVLJENO: Vraća samo error
		return fmt.Errorf("zapis sa ID '%s' nije pronađen ili je već obrisan u modulu '%s'", recordID, moduleDef.Name)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("greška pri potvrdi transakcije za brisanje u modulu '%s': %w", moduleDef.Name, err)
	}

	return nil
}

// GetRecordByID fetches a single record by its ID.
// This is used by GetSingleRecord in app.go
func (s *SQLDataset) GetRecordByID(moduleDef *ModuleDefinition, id interface{}) (map[string]interface{}, error) {
	pkCol := s.getPrimaryKeyColumn(moduleDef)
	if pkCol == nil {
		return nil, fmt.Errorf("modul '%s' nema definisan primarni ključ", moduleDef.Name)
	}

	columns := getVisibleDBColumnNames(moduleDef.Columns)
	if len(columns) == 0 {
		return nil, fmt.Errorf("modul '%s' nema definisanih vidljivih kolona za dohvatanje zapisa po ID-u", moduleDef.Name)
	}

	// Edit forma mora da zadrži primarni ključ čak i kada nije vidljiv,
	// inače se renderuje prazan /{recordID}/edit URL pri snimanju.
	hasPK := false
	for _, colName := range columns {
		if colName == pkCol.DBColumnName {
			hasPK = true
			break
		}
	}
	if !hasPK {
		columns = append([]string{pkCol.DBColumnName}, columns...)
	}

	// Kreiramo SELECT klauzulu sa aliasingom za svaku kolonu
	selectColumns := make([]string, len(columns))
	for i, colName := range columns {
		selectColumns[i] = fmt.Sprintf("%s AS %s", colName, colName)
	}

	query := fmt.Sprintf("SELECT %s FROM %s WHERE %s = $1",
		strings.Join(selectColumns, ", "),
		moduleDef.DBTableName,
		pkCol.DBColumnName,
	)

	query += " AND is_deleted = FALSE"

	log.Printf("DEBUG: Executing GetRecordByID query: %s with ID: %v", query, id)

	row := s.db.QueryRow(query, id)

	record := make(map[string]interface{})

	// Kreiramo dinamičke "destinacije" za Scan na osnovu vidljivih kolona
	// To osigurava da se slaže broj skeniranih kolona sa brojem kolona u upitu
	columnValues := make([]interface{}, len(columns))
	columnPointers := make([]interface{}, len(columns))
	for i := range columns {
		columnPointers[i] = &columnValues[i]
	}

	if err := row.Scan(columnPointers...); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("zapis sa ID '%v' nije pronađen u modulu '%s'", id, moduleDef.Name)
		}
		return nil, fmt.Errorf("greška pri skeniranju pojedinačnog reda: %w", err)
	}

	// Mapiramo skenirane vrednosti na mapu, koristeći dbColumnNames iz 'columns' slice-a
	for i, dbColName := range columns {
		val := columnValues[i]
		if val == nil {
			record[dbColName] = nil
		} else {
			switch v := val.(type) {
			case []byte:
				record[dbColName] = string(v)
			default:
				record[dbColName] = v
			}
		}
	}

	// Perform lookup expansion for this single record
	if err := s.performLookupExpansion([]map[string]interface{}{record}, moduleDef); err != nil {
		log.Printf("WARNING: Greška pri proširenju lookup-a za pojedinačni zapis u modulu '%s': %v", moduleDef.ID, err)
	}

	// Perform submodule expansion
	if len(moduleDef.SubModules) > 0 {
		// PK je već poznat kao id
		if err := s.performSubmoduleExpansion(record, moduleDef, id); err != nil {
			log.Printf("WARNING: Greška pri proširenju submodula za pojedinačni zapis '%v': %v", id, err)
		}
	}

	return record, nil
}

// GetRecordFieldByID fetches a single field value for a record by its ID.
func (s *SQLDataset) GetRecordFieldByID(moduleDef *ModuleDefinition, id interface{}, fieldName string) (interface{}, error) {
	pkCol := s.getPrimaryKeyColumn(moduleDef)
	if pkCol == nil {
		return nil, fmt.Errorf("modul '%s' nema definisan primarni ključ", moduleDef.Name)
	}

	colDef := getColumnByDBName(moduleDef.Columns, fieldName)
	if colDef == nil {
		return nil, fmt.Errorf("kolona '%s' ne postoji u modulu '%s'", fieldName, moduleDef.Name)
	}

	query := fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s = $1 AND is_deleted = FALSE",
		colDef.DBColumnName,
		moduleDef.DBTableName,
		pkCol.DBColumnName,
	)

	log.Printf("DEBUG: Executing GetRecordFieldByID query: %s with ID: %v", query, id)

	var value interface{}
	if err := s.db.QueryRow(query, id).Scan(&value); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("zapis sa ID '%v' nije pronađen u modulu '%s'", id, moduleDef.Name)
		}
		return nil, fmt.Errorf("greška pri dohvatanju polja '%s' iz zapisa: %w", fieldName, err)
	}

	if value == nil {
		return nil, nil
	}
	if v, ok := value.([]byte); ok {
		return string(v), nil
	}
	return value, nil
}

// getVisibleDBColumnNames helper to get only visible DB column names for SELECT query
func getVisibleDBColumnNames(cols []ColumnDefinition) []string {
	visibleCols := make([]string, 0)
	for _, col := range cols {
		// Dodaj DBColumnName samo ako je kolona vidljiva
		if col.IsVisible {
			visibleCols = append(visibleCols, col.DBColumnName)
		}
	}
	return visibleCols
}

// performLookupExpansion is now internal and part of GetRecords/GetRecordByID flow
func (s *SQLDataset) performLookupExpansion(records []map[string]interface{}, currentModule *ModuleDefinition) error {
	for _, colDef := range currentModule.Columns {
		// Proveri da li je kolona lookup tipa i da li ima definisan modul za lookup
		if colDef.Type == "lookup" && colDef.LookupModule != nil && colDef.LookupModuleID != "" {
			lookupModule := colDef.LookupModule
			lookupPKCol := s.getPrimaryKeyColumn(lookupModule)
			if lookupPKCol == nil {
				log.Printf("WARNING: Lookup modul '%s' za kolonu '%s' nema definisan primarni ključ, preskačem proširenje", lookupModule.ID, colDef.Name)
				continue
			}

			// Sakupi sve jedinstvene lookup ID-eve iz trenutnih zapisa
			lookupIDs := make(map[interface{}]struct{})
			for _, record := range records {
				if id, ok := record[colDef.DBColumnName]; ok && id != nil {
					lookupIDs[id] = struct{}{}
				}
			}

			if len(lookupIDs) == 0 {
				continue // Nema lookup ID-eva za obradu u ovoj koloni
			}

			// Pripremi WHERE klauzulu za batch dohvatanje lookup podataka
			placeholders := make([]string, 0, len(lookupIDs))
			args := make([]interface{}, 0, len(lookupIDs))
			paramCounter := 1
			for id := range lookupIDs {
				placeholders = append(placeholders, fmt.Sprintf("$%d", paramCounter))
				args = append(args, id)
				paramCounter++
			}

			// Odaberi kolone za lookup. Koristi LookupDisplayField ako je definisan
			lookupColsToSelect := []string{lookupPKCol.DBColumnName}
			lookupDisplayCol := ""

			if colDef.LookupDisplayField != "" { // Koristi LookupDisplayField
				lookupDisplayCol = colDef.LookupDisplayField
			} else {
				// Fallback na prvu string kolonu (ili "name" ako postoji)
				for _, lc := range lookupModule.Columns {
					if lc.DBColumnName == "name" && lc.Type == "string" {
						lookupDisplayCol = "name"
						break
					}
				}
				if lookupDisplayCol == "" {
					for _, lc := range lookupModule.Columns {
						if lc.DBColumnName != lookupPKCol.DBColumnName && lc.Type == "string" {
							lookupDisplayCol = lc.DBColumnName
							break
						}
					}
				}
				if lookupDisplayCol == "" {
					lookupDisplayCol = lookupPKCol.DBColumnName // Fallback na ID ako nema string kolone
				}
			}

			// Dodaj prikaznu kolonu u SELECT listu, ako već nije primarni ključ
			if lookupDisplayCol != lookupPKCol.DBColumnName {
				lookupColsToSelect = append(lookupColsToSelect, lookupDisplayCol)
			}

			lookupQuery := fmt.Sprintf("SELECT %s FROM %s WHERE %s IN (%s)",
				strings.Join(lookupColsToSelect, ", "),
				lookupModule.DBTableName,
				lookupPKCol.DBColumnName,
				strings.Join(placeholders, ", "),
			)

			lookupRows, err := s.db.Query(lookupQuery, args...)
			if err != nil {
				return fmt.Errorf("greška pri dohvatanju lookup podataka za kolonu '%s': %w", colDef.Name, err)
			}
			defer lookupRows.Close()

			// Mapiraj lookup ID-eve na dohvataene objekte
			lookupMap := make(map[interface{}]map[string]interface{})
			for lookupRows.Next() {
				lookupCols, err := lookupRows.Columns()
				if err != nil {
					return fmt.Errorf("greška pri čitanju naziva kolona lookup-a: %w", err)
				}
				values := make([]interface{}, len(lookupCols))
				pointers := make([]interface{}, len(lookupCols))
				for i := range values {
					pointers[i] = &values[i]
				}

				err = lookupRows.Scan(pointers...)
				if err != nil {
					return fmt.Errorf("greška pri skeniranju lookup reda: %w", err)
				}

				lookupRecord := make(map[string]interface{})
				for i, colName := range lookupCols {
					val := values[i]
					if b, ok := val.([]byte); ok {
						val = string(b)
					}
					lookupRecord[colName] = val
				}
				if id, ok := lookupRecord[lookupPKCol.DBColumnName]; ok {
					lookupMap[id] = lookupRecord
				}
			}
			if err = lookupRows.Err(); err != nil {
				return fmt.Errorf("greška nakon iteracije lookup redova: %w", err)
			}

			// Ažuriraj originalne zapise sa proširenim lookup podacima
			for idx := range records {
				record := records[idx]
				if id, ok := record[colDef.DBColumnName]; ok && id != nil {
					if expandedVal, found := lookupMap[id]; found {
						lookupObject := map[string]interface{}{
							"id": id,
						}
						if val, ok := expandedVal[lookupDisplayCol]; ok {
							lookupObject["name"] = val
						} else {
							lookupObject["name"] = fmt.Sprintf("ID: %v", id)
						}
						records[idx][colDef.DBColumnName] = lookupObject
					} else {
						records[idx][colDef.DBColumnName] = nil
					}
				}
			}
		}
	}
	return nil
}

// performSubmoduleExpansion fetches and attaches submodule data to a parent record.
func (s *SQLDataset) performSubmoduleExpansion(parentRecord map[string]interface{}, parentModuleDef *ModuleDefinition, parentPKVal interface{}) error {
	for _, subModDef := range parentModuleDef.SubModules {
		targetModule := s.config.GetModuleByID(subModDef.TargetModuleID)
		if targetModule == nil {
			log.Printf("WARNING: Target modul '%s' za submodul '%s' nije pronađen.", subModDef.TargetModuleID, subModDef.DisplayName)
			continue
		}

		columns := getVisibleDBColumnNames(targetModule.Columns) // Koristi pomoćnu funkciju i ovde
		if len(columns) == 0 {
			log.Printf("WARNING: Submodul '%s' (modul '%s') nema definisanih vidljivih kolona.", subModDef.DisplayName, targetModule.ID)
			continue
		}

		// Kreiramo SELECT klauzulu sa aliasingom za svaku kolonu
		selectColumns := make([]string, len(columns))
		for i, colName := range columns {
			selectColumns[i] = fmt.Sprintf("%s AS %s", colName, colName)
		}

		query := fmt.Sprintf("SELECT %s FROM %s WHERE %s = $1",
			strings.Join(selectColumns, ", "),
			targetModule.DBTableName,
			subModDef.ChildForeignKeyField,
		)

		log.Printf("DEBUG: Executing submodule query for '%s': %s with parent PK: %v", subModDef.DisplayName, query, parentPKVal)

		rows, err := s.db.Query(query, parentPKVal)
		if err != nil {
			return fmt.Errorf("greška pri dohvatanju podataka za submodul '%s': %w", subModDef.DisplayName, err)
		}
		defer rows.Close()

		var subRecords []map[string]interface{}
		for rows.Next() {
			subRecord := make(map[string]interface{})

			// Dohvati stvarne nazive kolona iz baze za submodul
			dbColumnNames, err := rows.Columns()
			if err != nil {
				return fmt.Errorf("greška pri dohvatanju naziva kolona submodula iz baze: %w", err)
			}

			columnValues := make([]interface{}, len(dbColumnNames))
			columnPointers := make([]interface{}, len(dbColumnNames))
			for i := range dbColumnNames {
				columnPointers[i] = &columnValues[i]
			}

			if err := rows.Scan(columnPointers...); err != nil {
				return fmt.Errorf("greška pri skeniranju reda submodula '%s': %w", subModDef.DisplayName, err)
			}

			for i, dbColName := range dbColumnNames {
				val := columnValues[i]
				if val == nil {
					subRecord[dbColName] = nil
				} else {
					switch v := val.(type) {
					case []byte:
						subRecord[dbColName] = string(v)
					default:
						subRecord[dbColName] = v
					}
				}
			}
			if err := s.performLookupExpansion([]map[string]interface{}{subRecord}, targetModule); err != nil {
				log.Printf("WARNING: Greška pri proširenju lookup-a u submodulu '%s': %v", subModDef.DisplayName, err)
			}
			if len(targetModule.SubModules) > 0 {
				if subPKCol := s.getPrimaryKeyColumn(targetModule); subPKCol != nil {
					if subPKVal, ok := subRecord[subPKCol.DBColumnName]; ok {
						if err := s.performSubmoduleExpansion(subRecord, targetModule, subPKVal); err != nil {
							log.Printf("WARNING: Greška pri rekurzivnom proširenju submodula '%s' unutar '%s': %v", subModDef.DisplayName, parentModuleDef.ID, err)
						}
					}
				}
			}
			subRecords = append(subRecords, subRecord)
		}

		if err = rows.Err(); err != nil {
			return fmt.Errorf("greška nakon iteracije kroz redove submodula '%s': %w", subModDef.DisplayName, err)
		}

		parentRecord[subModDef.TargetModuleID] = subRecords
	}
	return nil
}

func (s *SQLDataset) performSubmoduleExpansionBatch(parentRecords []map[string]interface{}, parentModuleDef *ModuleDefinition, parentPKField string) error {
	if len(parentRecords) == 0 {
		return nil
	}

	parentByID := make(map[string]map[string]interface{}, len(parentRecords))
	parentIDs := make([]interface{}, 0, len(parentRecords))
	for _, parentRecord := range parentRecords {
		parentPKVal, ok := parentRecord[parentPKField]
		if !ok || parentPKVal == nil {
			continue
		}
		key := fmt.Sprint(parentPKVal)
		parentByID[key] = parentRecord
		parentIDs = append(parentIDs, parentPKVal)
	}

	if len(parentIDs) == 0 {
		return nil
	}

	for _, subModDef := range parentModuleDef.SubModules {
		targetModule := s.config.GetModuleByID(subModDef.TargetModuleID)
		if targetModule == nil {
			log.Printf("WARNING: Target modul '%s' za submodul '%s' nije pronađen.", subModDef.TargetModuleID, subModDef.DisplayName)
			continue
		}

		columns := getVisibleDBColumnNames(targetModule.Columns)
		if len(columns) == 0 {
			log.Printf("WARNING: Submodul '%s' (modul '%s') nema definisanih vidljivih kolona.", subModDef.DisplayName, targetModule.ID)
			continue
		}

		if !containsString(columns, subModDef.ChildForeignKeyField) {
			columns = append([]string{subModDef.ChildForeignKeyField}, columns...)
		}

		if subPKCol := s.getPrimaryKeyColumn(targetModule); subPKCol != nil && !containsString(columns, subPKCol.DBColumnName) {
			columns = append([]string{subPKCol.DBColumnName}, columns...)
		}

		selectColumns := make([]string, len(columns))
		for i, colName := range columns {
			selectColumns[i] = fmt.Sprintf("%s AS %s", colName, colName)
		}

		placeholders := make([]string, len(parentIDs))
		for i := range parentIDs {
			placeholders[i] = fmt.Sprintf("$%d", i+1)
		}

		query := fmt.Sprintf(
			"SELECT %s FROM %s WHERE %s IN (%s) AND is_deleted = FALSE",
			strings.Join(selectColumns, ", "),
			targetModule.DBTableName,
			subModDef.ChildForeignKeyField,
			strings.Join(placeholders, ", "),
		)

		log.Printf("DEBUG: Executing batch submodule query for '%s': %s with parent PKs: %v", subModDef.DisplayName, query, parentIDs)

		rows, err := s.db.Query(query, parentIDs...)
		if err != nil {
			return fmt.Errorf("greška pri batch dohvatanju podataka za submodul '%s': %w", subModDef.DisplayName, err)
		}

		dbColumnNames, err := rows.Columns()
		if err != nil {
			rows.Close()
			return fmt.Errorf("greška pri dohvatanju naziva kolona submodula iz baze: %w", err)
		}

		grouped := make(map[string][]map[string]interface{})
		var allSubRecords []map[string]interface{}
		for rows.Next() {
			subRecord := make(map[string]interface{})
			columnValues := make([]interface{}, len(dbColumnNames))
			columnPointers := make([]interface{}, len(dbColumnNames))
			for i := range dbColumnNames {
				columnPointers[i] = &columnValues[i]
			}

			if err := rows.Scan(columnPointers...); err != nil {
				rows.Close()
				return fmt.Errorf("greška pri skeniranju reda submodula '%s': %w", subModDef.DisplayName, err)
			}

			for i, dbColName := range dbColumnNames {
				val := columnValues[i]
				if val == nil {
					subRecord[dbColName] = nil
					continue
				}
				switch v := val.(type) {
				case []byte:
					subRecord[dbColName] = string(v)
				default:
					subRecord[dbColName] = v
				}
			}

			fkVal, ok := subRecord[subModDef.ChildForeignKeyField]
			if !ok || fkVal == nil {
				continue
			}
			grouped[fmt.Sprint(fkVal)] = append(grouped[fmt.Sprint(fkVal)], subRecord)
			allSubRecords = append(allSubRecords, subRecord)
		}

		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("greška nakon iteracije kroz redove submodula '%s': %w", subModDef.DisplayName, err)
		}
		rows.Close()

		if err := s.performLookupExpansion(allSubRecords, targetModule); err != nil {
			log.Printf("WARNING: Greška pri batch proširenju lookup-a u submodulu '%s': %v", subModDef.DisplayName, err)
		}

		for _, subRecord := range allSubRecords {
			if len(targetModule.SubModules) == 0 {
				continue
			}
			subPKCol := s.getPrimaryKeyColumn(targetModule)
			if subPKCol == nil {
				continue
			}
			subPKVal, ok := subRecord[subPKCol.DBColumnName]
			if !ok || subPKVal == nil {
				continue
			}
			if err := s.performSubmoduleExpansion(subRecord, targetModule, subPKVal); err != nil {
				log.Printf("WARNING: Greška pri rekurzivnom proširenju submodula '%s' unutar '%s': %v", subModDef.DisplayName, parentModuleDef.ID, err)
			}
		}

		for parentKey, parentRecord := range parentByID {
			parentRecord[subModDef.TargetModuleID] = grouped[parentKey]
		}
	}

	return nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// ========== RBAC Funkcije ==========

func allPermissionsMask() int64 {
	return PermissionRead |
		PermissionCreate |
		PermissionUpdate |
		PermissionDelete |
		PermissionExecute |
		PermissionExport |
		PermissionImport |
		PermissionApprove
}

func (s *SQLDataset) getSupportedPermissionsMask(resource string) (int64, error) {
	requiredPermissions, found, err := s.getResourceRequiredPermissions(resource)
	if err != nil {
		return 0, err
	}
	if found {
		return requiredPermissions, nil
	}

	// Fallback za slučaj kada resources tabela još nije sinhronizovana.
	if moduleDef := s.config.GetModuleByID(resource); moduleDef != nil {
		return moduleDef.SupportedPermissions(), nil
	}

	return allPermissionsMask(), nil
}

func (s *SQLDataset) isUserAdmin(userID int64) (bool, error) {
	var isAdmin bool
	err := s.db.QueryRow("SELECT is_admin FROM users WHERE id = $1 AND is_deleted = FALSE", userID).Scan(&isAdmin)
	if err == sql.ErrNoRows {
		return false, fmt.Errorf("korisnik sa ID '%d' nije pronađen", userID)
	}
	if err != nil {
		return false, fmt.Errorf("greška pri dohvatanju korisnika '%d': %w", userID, err)
	}
	return isAdmin, nil
}

func (s *SQLDataset) getRolePermissionMask(userID int64, resource string) (int64, error) {
	rows, err := s.db.Query(`
		SELECT rp.permissions FROM role_permissions rp
		JOIN user_roles ur ON rp.role_id = ur.role_id
		WHERE ur.user_id = $1 AND rp.resource = $2
	`, userID, resource)
	if err != nil {
		return 0, fmt.Errorf("greška pri dohvatanju dozvola za korisnika '%d' na resursu '%s': %w", userID, resource, err)
	}
	defer rows.Close()

	var permissions int64
	for rows.Next() {
		var rolePerm int64
		if err := rows.Scan(&rolePerm); err != nil {
			return 0, fmt.Errorf("greška pri skeniranju dozvola: %w", err)
		}
		permissions |= rolePerm
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("greška pri iteraciji kroz dozvole: %w", err)
	}

	return permissions, nil
}

func (s *SQLDataset) getGrantedPermissionsMask(userID int64, resource string) (int64, error) {
	isAdmin, err := s.isUserAdmin(userID)
	if err != nil {
		return 0, err
	}

	if isAdmin {
		return allPermissionsMask(), nil
	}

	return s.getRolePermissionMask(userID, resource)
}

// GetUserByUsername pronalazi korisnika po korisničkom imenu
func (s *SQLDataset) CreateUser(username, email, passwordHash string, isAdmin bool) (int64, error) {
	var id int64
	err := s.db.QueryRow(
		`INSERT INTO users (username, email, password_hash, is_admin, created_at)
		VALUES ($1, $2, $3, $4, NOW()) RETURNING id`,
		username, email, passwordHash, isAdmin,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("greška pri kreiranju korisnika: %w", err)
	}
	return id, nil
}

func (s *SQLDataset) GetUserByUsername(username string) (*User, error) {
	var user User
	err := s.db.QueryRow(
		"SELECT id, username, password_hash, is_admin, created_at FROM users WHERE username = $1 AND is_deleted = FALSE",
		username,
	).Scan(&user.ID, &user.Username, &user.PasswordHash, &user.IsAdmin, &user.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil // Korisnik ne postoji
	}
	if err != nil {
		return nil, fmt.Errorf("greška pri dohvatanju korisnika '%s': %w", username, err)
	}
	return &user, nil
}

// GetUserPermissions vraća bitmask dozvola korisnika za zadati resurs (modul)
func (s *SQLDataset) GetUserPermissions(userID int64, resource string) (int64, error) {
	return s.getGrantedPermissionsMask(userID, resource)
}

func (s *SQLDataset) getResourceRequiredPermissions(resource string) (int64, bool, error) {
	var required int64
	err := s.db.QueryRow(
		"SELECT required_permissions FROM resources WHERE resource = $1",
		resource,
	).Scan(&required)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("greška pri čitanju required dozvola za resurs '%s': %w", resource, err)
	}

	return required, true, nil
}

// HasPermission proverava da li korisnik ima specifičnu dozvolu na resursu
func (s *SQLDataset) HasPermission(userID int64, resource string, permission int64) (bool, error) {
	supportedPermissions, err := s.getSupportedPermissionsMask(resource)
	if err != nil {
		return false, err
	}

	if (supportedPermissions & permission) == 0 {
		return false, nil
	}

	grantedPermissions, err := s.getGrantedPermissionsMask(userID, resource)
	if err != nil {
		return false, err
	}

	effectivePermissions := grantedPermissions & supportedPermissions
	return (effectivePermissions & permission) != 0, nil
}

// AddUserToRole dodeljuje rolu korisniku
func (s *SQLDataset) AddUserToRole(userID, roleID int64) error {
	_, err := s.db.Exec(
		"INSERT INTO user_roles (user_id, role_id) VALUES ($1, $2) ON CONFLICT DO NOTHING",
		userID, roleID,
	)
	if err != nil {
		return fmt.Errorf("greška pri dodavanju korisnika '%d' u rolu '%d': %w", userID, roleID, err)
	}
	return nil
}

// SetRolePermissions postavlja dozvole za rolu na resursu
func (s *SQLDataset) SetRolePermissions(roleID int64, resource string, permissions int64) error {
	_, err := s.db.Exec(`
		INSERT INTO role_permissions (role_id, resource, permissions)
		VALUES ($1, $2, $3)
		ON CONFLICT (role_id, resource) DO UPDATE
		SET permissions = EXCLUDED.permissions
	`, roleID, resource, permissions)
	if err != nil {
		return fmt.Errorf("greška pri postavljanju dozvola za rolu '%d' na resursu '%s': %w", roleID, resource, err)
	}
	return nil
}
