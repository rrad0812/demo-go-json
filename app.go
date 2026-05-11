// app.go
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// AppConfig stores application-wide configuration and compiled assets.
type AppConfig struct {
	Config                Config
	Modules               map[string]*ModuleDefinition
	LookupTables          map[string]map[interface{}]map[string]interface{} // Not currently used but good to keep if planned
	compiledRegexes       map[string]*regexp.Regexp                         // Mapa za prekompilirane regex-e
	ReverseLookupMappings map[string]map[interface{}]string                 // Not currently used but good to keep if planned
}

// NewAppConfig creates and initializes a new AppConfig.
func NewAppConfig(cfg *Config) (*AppConfig, error) {
	appCfg := &AppConfig{
		Config:  *cfg, // Direktno dodeljivanje Config strukture
		Modules: make(map[string]*ModuleDefinition),
	}

	if err := appCfg.LoadModules(); err != nil {
		return nil, fmt.Errorf("greška pri učitavanju modula: %w", err)
	}

	if err := appCfg.ValidateModules(); err != nil {
		return nil, fmt.Errorf("greška pri validaciji modula: %w", err)
	}

	appCfg.ResolveModuleLookups()       // Resolve module references after loading all modules
	appCfg.ResolveSubmoduleReferences() // Resolve submodule references
	appCfg.CompileRegexes()             // Kompilira regex obrasce

	return appCfg, nil
}

// LoadModules loads module definitions from JSON files.
func (ac *AppConfig) LoadModules() error {
	modulesDir := ac.Config.ModulesPath
	files, err := os.ReadDir(modulesDir)
	if err != nil {
		return fmt.Errorf("greška pri čitanju direktorijuma modula '%s': %w", modulesDir, err)
	}

	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(file.Name(), ".json") {
			filePath := filepath.Join(modulesDir, file.Name())
			content, err := os.ReadFile(filePath)
			if err != nil {
				log.Printf("WARNING: Greška pri čitanju fajla modula '%s': %v", filePath, err)
				continue
			}

			var moduleDef ModuleDefinition
			if err := json.Unmarshal(content, &moduleDef); err != nil {
				log.Printf("WARNING: Greška pri parsiranju modula iz fajla '%s': %v", filePath, err)
				continue
			}

			if moduleDef.ID == "" {
				log.Printf("WARNING: Modul u fajlu '%s' nema definisan ID, preskačem.", filePath)
				continue
			}

			ac.Modules[moduleDef.ID] = &moduleDef
			log.Printf("INFO: Učitan modul: %s (ID: %s)", moduleDef.Name, moduleDef.ID)
		}
	}

	if len(ac.Modules) == 0 {
		return fmt.Errorf("nije pronađen nijedan modul u direktorijumu '%s'", modulesDir)
	}

	return nil
}

// GetModuleByID retrieves a module definition by its ID.
func (ac *AppConfig) GetModuleByID(moduleID string) *ModuleDefinition {
	if module, ok := ac.Modules[moduleID]; ok {
		return module
	}
	return nil
}

// ValidateModules validates loaded module definitions and fails fast on bad config.
func (ac *AppConfig) ValidateModules() error {
	allowedTypes := map[ModuleType]struct{}{
		ModuleTypeModule: {},
		ModuleTypeReport: {},
		ModuleTypeGroup:  {},
		ModuleTypeRoot:   {},
		ModuleTypeSystem: {},
		ModuleTypeCustom: {},
	}

	moduleIDs := make([]string, 0, len(ac.Modules))
	for id := range ac.Modules {
		moduleIDs = append(moduleIDs, id)
	}
	sort.Strings(moduleIDs)

	var errs []string
	for _, moduleID := range moduleIDs {
		mod := ac.Modules[moduleID]
		if mod == nil {
			errs = append(errs, fmt.Sprintf("modul '%s' je nil", moduleID))
			continue
		}

		if _, ok := allowedTypes[mod.Type]; !ok {
			errs = append(errs, fmt.Sprintf("modul '%s' ima neispravan type '%s'", mod.ID, mod.Type))
		}

		if mod.Type == ModuleTypeModule {
			if !mod.IsTable {
				errs = append(errs, fmt.Sprintf("modul '%s' sa type='%s' mora imati is_table=true", mod.ID, ModuleTypeModule))
			}
			if strings.TrimSpace(mod.DBTableName) == "" {
				errs = append(errs, fmt.Sprintf("modul '%s' sa type='%s' mora imati db_table_name", mod.ID, ModuleTypeModule))
			}
		}

		if mod.IsTable {
			if strings.TrimSpace(mod.DBTableName) == "" {
				errs = append(errs, fmt.Sprintf("modul '%s' sa is_table=true mora imati db_table_name", mod.ID))
			}
			if !moduleHasPrimaryKey(mod) {
				errs = append(errs, fmt.Sprintf("modul '%s' sa is_table=true mora imati bar jednu is_primary_key kolonu", mod.ID))
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}

	return nil
}

func moduleHasPrimaryKey(mod *ModuleDefinition) bool {
	for _, col := range mod.Columns {
		if col.IsPrimaryKey {
			return true
		}
	}

	return false
}

// ResolveModuleLookups resolves lookupModule pointers in ColumnDefinition.
func (ac *AppConfig) ResolveModuleLookups() {
	for _, module := range ac.Modules {
		for i := range module.Columns {
			col := &module.Columns[i] // Get a pointer to the column
			if col.Type == "lookup" && col.LookupModuleID != "" {
				if lookupModule := ac.GetModuleByID(col.LookupModuleID); lookupModule != nil {
					col.LookupModule = lookupModule
					log.Printf("INFO: Razrešen lookup za kolonu '%s' u modulu '%s' -> Modul '%s'", col.Name, module.ID, lookupModule.ID)
				} else {
					log.Printf("WARNING: Lookup modul sa ID '%s' nije pronađen za kolonu '%s' u modulu '%s'.", col.LookupModuleID, col.Name, module.ID)
				}
			}
		}
	}
}

// ResolveSubmoduleReferences resolves TargetModule pointers in SubModuleDefinition.
func (ac *AppConfig) ResolveSubmoduleReferences() {
	for _, module := range ac.Modules {
		for i := range module.SubModules {
			subMod := &module.SubModules[i] // Get a pointer to the submodule
			if subMod.TargetModuleID != "" {
				if targetModule := ac.GetModuleByID(subMod.TargetModuleID); targetModule != nil {
					subMod.TargetModule = targetModule
					log.Printf("INFO: Razrešen submodul '%s' u modulu '%s' -> Target Modul '%s'", subMod.DisplayName, module.ID, targetModule.ID)
				} else {
					log.Printf("WARNING: Target modul sa ID '%s' nije pronađen za submodul '%s' u modulu '%s'.", subMod.TargetModuleID, subMod.DisplayName, module.ID)
				}
			}
		}
	}
}

// CompileRegexes compiles all regex patterns defined in modules for performance.
func (ac *AppConfig) CompileRegexes() {
	ac.compiledRegexes = make(map[string]*regexp.Regexp)

	// Dodaj email regex za kompilaciju
	// Koristićemo ključ "emailValidation" da bismo ga razlikovali od drugih
	ac.compileAndStoreRegex("emailValidation", `^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)

	for _, module := range ac.Modules {
		for _, col := range module.Columns {
			if strings.Contains(col.Validation, "regex:") {
				parts := strings.Split(col.Validation, ",")
				for _, part := range parts {
					rule := strings.TrimSpace(part)
					if strings.HasPrefix(rule, "regex:") {
						pattern := strings.TrimPrefix(rule, "regex:")
						ac.compileAndStoreRegex(pattern, pattern) // Koristi pattern kao ključ i vrednost
					}
				}
			}
		}
	}
	log.Println("INFO: Regex obrasci uspešno kompilirani.")
}

// GetDatabaseConfig retrieves the DatabaseConfig from AppConfig.
func (ac *AppConfig) GetDatabaseConfig() DatabaseConfig {
	return ac.Config.Database
}

// compileAndStoreRegex is a helper to compile and store a regex.
func (ac *AppConfig) compileAndStoreRegex(key, pattern string) {
	if _, exists := ac.compiledRegexes[key]; exists {
		return // Already compiled
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		log.Printf("ERROR: Greška pri kompilaciji regex obrasca za ključ '%s' (pattern: '%s'): %v", key, pattern, err)
		return
	}
	ac.compiledRegexes[key] = re
}

// GetCompiledRegex retrieves a compiled regex by its key.
func (ac *AppConfig) GetCompiledRegex(key string) (*regexp.Regexp, bool) {
	re, found := ac.compiledRegexes[key]
	return re, found
}
