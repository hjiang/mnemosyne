// Package main is the mnemosyne CLI entrypoint.
package main

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/hjiang/mnemosyne/internal/accounts"
	"github.com/hjiang/mnemosyne/internal/auth"
	"github.com/hjiang/mnemosyne/internal/backup"
	"github.com/hjiang/mnemosyne/internal/blobs"
	"github.com/hjiang/mnemosyne/internal/config"
	"github.com/hjiang/mnemosyne/internal/db"
	"github.com/hjiang/mnemosyne/internal/httpserver"
	"github.com/hjiang/mnemosyne/internal/messages"
	"github.com/hjiang/mnemosyne/internal/search"
	"github.com/hjiang/mnemosyne/internal/users"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mnemosyne <serve|adduser> [options]")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		if err := runServe(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	case "adduser":
		if err := runAddUser(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func loadConfig() (config.Config, error) {
	cfgPath := os.Getenv("MNEMOSYNE_CONFIG")
	if cfgPath == "" {
		cfgPath = "/etc/mnemosyne/config.yaml"
	}
	// If no config file exists, use defaults with env overrides.
	cfgPath = filepath.Clean(cfgPath)
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		cfg := config.Defaults()
		config.ApplyEnvOverrides(&cfg)
		return cfg, nil
	}
	return config.Load(cfgPath)
}

func openDB(cfg config.Config) (*sql.DB, error) {
	// Ensure data dir exists.
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return nil, fmt.Errorf("creating data dir: %w", err)
	}
	dbPath := filepath.Join(cfg.DataDir, "mnemosyne.db")
	return db.Open(dbPath)
}

func runServe() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	database, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()

	if err := db.Migrate(database); err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}

	km, err := accounts.NewKeyManager(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("initializing key manager: %w", err)
	}

	userRepo := users.NewRepo(database, time.Now)
	sessions := auth.NewSessionStore(database, time.Now, cfg.SessionTTL)
	acctRepo := accounts.NewRepo(database, km)
	msgRepo := messages.NewRepo(database)
	blobStore := blobs.NewStore(filepath.Join(cfg.DataDir, "blobs"))
	orch := backup.NewOrchestrator(acctRepo, msgRepo, blobStore)
	searchExec := search.NewExecutor(database)
	srv := httpserver.New(userRepo, sessions, acctRepo, orch, searchExec, blobStore)

	// Backfill FTS index for any unindexed messages.
	go backfillFTS(msgRepo)

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP shutdown error: %v", err)
		}
	}()

	fmt.Printf("listening on %s\n", cfg.Listen)
	if err := httpSrv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

func backfillFTS(msgRepo *messages.Repo) {
	msgs, err := msgRepo.ListUnindexedMessages()
	if err != nil {
		log.Printf("FTS backfill: listing failed: %v", err)
		return
	}
	if len(msgs) == 0 {
		return
	}
	log.Printf("FTS backfill: indexing %d messages", len(msgs))
	for _, m := range msgs {
		rowid, err := msgRepo.GetRowID(m.Hash)
		if err != nil {
			continue
		}
		_ = msgRepo.IndexFTS(rowid, m.Subject, m.FromAddr, m.ToAddrs, m.CcAddrs, m.BodyText)
	}
	log.Printf("FTS backfill: done")
}

func runAddUser() error {
	if len(os.Args) < 3 {
		return fmt.Errorf("usage: mnemosyne adduser <email>")
	}
	email := os.Args[2]

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	database, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()

	if err := db.Migrate(database); err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}

	fmt.Print("Password: ")
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return fmt.Errorf("reading password")
	}
	password := scanner.Text()

	if password == "" {
		return fmt.Errorf("password must not be empty")
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}

	userRepo := users.NewRepo(database, time.Now)
	u, err := userRepo.Create(email, hash)
	if err != nil {
		return fmt.Errorf("creating user: %w", err)
	}

	fmt.Printf("created user %q (id=%d)\n", u.Email, u.ID)
	return nil
}
