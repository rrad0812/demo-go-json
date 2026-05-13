// main.go
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	// Učitavanje konfiguracije
	config, err := LoadConfigFromFile("config.json")
	if err != nil {
		log.Fatalf("Fatal: Greška pri učitavanju konfiguracije: %v", err)
	}

	// Inicijalizacija AppConfig
	appConfig, err := NewAppConfig(config)
	if err != nil {
		log.Fatalf("Fatal: Greška pri inicijalizaciji AppConfig: %v", err)
	}

	// Inicijalizacija baze podataka
	dataset, err := NewSQLDataset(appConfig)
	if err != nil {
		log.Fatalf("Fatal: Greška pri inicijalizaciji baze podataka: %v", err)
	}
	defer dataset.Close() // Zatvara vezu sa bazom podataka kada se main završi

	migrationDryRun := parseBoolEnv(os.Getenv("MIGRATIONS_DRY_RUN"))
	SetMigrationDryRun(migrationDryRun)
	if migrationDryRun {
		log.Println("INFO: MIGRATIONS_DRY_RUN=true, server se neće pokrenuti nakon planiranja migracija.")
	}

	// Pokretanje migracija (kreira/ažurira tabele na osnovu JSON definicija)
	if err := dataset.RunMigrations(); err != nil {
		log.Fatalf("Fatal: Greška pri pokretanju migracija: %v", err)
	}

	if migrationDryRun {
		log.Println("INFO: Dry-run migracija završen. Gašenje procesa bez pokretanja API servera.")
		return
	}

	// Inicijalizacija API servera
	apiServer := NewAPIServer(appConfig, dataset) // Kreiramo instancu APIServera

	// Postavljanje HTTP servera
	serverAddr := ":8080"
	if port := strings.TrimSpace(os.Getenv("PORT")); port != "" {
		serverAddr = ":" + port
	}
	srv := &http.Server{
		Addr:    serverAddr,
		Handler: apiServer.router, // Sada koristimo router iz APIServer instance
		// Dobra praksa je postaviti timeout-e
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Pokretanje servera u gorutini
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			// Logovanje greške ako server ne uspe da se pokrene (npr. port je zauzet)
			log.Fatalf("Fatal: Greška pri pokretanju servera: %v", err)
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("INFO: Gašenje servera...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("Fatal: Server se nije ugasio gracefuly: %v", err)
	}

	log.Println("INFO: Server je ugašen.")
}

func parseBoolEnv(raw string) bool {
	v := strings.ToLower(strings.TrimSpace(raw))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}
