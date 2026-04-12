package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestOpen_WALAndForeignKeys(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()

	var journalMode string
	if err := database.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want %q", journalMode, "wal")
	}

	var fk int
	if err := database.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
}

func TestOpen_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "new.db")

	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = database.Close()
}

func TestMigrate_AppliesAll(t *testing.T) {
	dir := t.TempDir()
	database, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()

	if err := Migrate(database); err != nil {
		t.Fatal(err)
	}

	// Verify users table exists.
	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		t.Fatalf("users table missing: %v", err)
	}

	// Verify sessions table exists.
	if err := database.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&count); err != nil {
		t.Fatalf("sessions table missing: %v", err)
	}

	// Verify migration was recorded.
	if err := database.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = 1").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("schema_migrations has %d rows for version 1, want 1", count)
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	dir := t.TempDir()
	database, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()

	if err := Migrate(database); err != nil {
		t.Fatal(err)
	}

	var countAfterFirst int
	if err := database.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&countAfterFirst); err != nil {
		t.Fatal(err)
	}

	// Run again — should be a no-op.
	if err := Migrate(database); err != nil {
		t.Fatalf("second Migrate failed: %v", err)
	}

	var countAfterSecond int
	if err := database.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&countAfterSecond); err != nil {
		t.Fatal(err)
	}
	if countAfterFirst != countAfterSecond {
		t.Errorf("migration count changed: %d → %d (not idempotent)", countAfterFirst, countAfterSecond)
	}
	if countAfterFirst == 0 {
		t.Error("expected at least one migration record")
	}
}

func TestMigrate_ForeignKeysWork(t *testing.T) {
	dir := t.TempDir()
	database, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()

	if err := Migrate(database); err != nil {
		t.Fatal(err)
	}

	// Inserting a session with a nonexistent user_id should fail.
	_, err = database.Exec(
		"INSERT INTO sessions (id, user_id, created_at, expires_at) VALUES (x'00', 9999, 0, 0)",
	)
	if err == nil {
		t.Fatal("expected foreign key violation inserting session with bad user_id")
	}
}

func TestMigrateFS_BadSQL(t *testing.T) {
	dir := t.TempDir()
	database, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()

	badFS := fstest.MapFS{
		"m/0001_bad.sql": &fstest.MapFile{Data: []byte("NOT VALID SQL ;;;")},
	}
	err = MigrateFS(database, badFS, "m")
	if err == nil {
		t.Fatal("expected error for invalid SQL migration")
	}

	// Verify the bad migration was NOT recorded (rolled back).
	var count int
	_ = database.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = 1").Scan(&count)
	if count != 0 {
		t.Errorf("bad migration was recorded in schema_migrations")
	}
}

func TestMigrateFS_InvalidVersionNumber(t *testing.T) {
	dir := t.TempDir()
	database, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()

	badFS := fstest.MapFS{
		"m/abc_bad.sql": &fstest.MapFile{Data: []byte("SELECT 1")},
	}
	err = MigrateFS(database, badFS, "m")
	if err == nil {
		t.Fatal("expected error for invalid version number in migration filename")
	}
}

func TestMigrateFS_SkipsNonSQL(t *testing.T) {
	dir := t.TempDir()
	database, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()

	mixedFS := fstest.MapFS{
		"m/0001_init.sql": &fstest.MapFile{Data: []byte("CREATE TABLE test_table (id INTEGER PRIMARY KEY)")},
		"m/README.md":     &fstest.MapFile{Data: []byte("not a migration")},
	}
	err = MigrateFS(database, mixedFS, "m")
	if err != nil {
		t.Fatal(err)
	}

	// Verify only the SQL migration was applied.
	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM test_table").Scan(&count); err != nil {
		t.Fatalf("test_table missing: %v", err)
	}
}

func TestMigrateFS_AppliesInOrder(t *testing.T) {
	dir := t.TempDir()
	database, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()

	// Migration 2 depends on migration 1's table.
	orderedFS := fstest.MapFS{
		"m/0001_create.sql": &fstest.MapFile{Data: []byte("CREATE TABLE parent (id INTEGER PRIMARY KEY)")},
		"m/0002_child.sql":  &fstest.MapFile{Data: []byte("CREATE TABLE child (id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES parent(id))")},
	}
	err = MigrateFS(database, orderedFS, "m")
	if err != nil {
		t.Fatal(err)
	}

	var count int
	_ = database.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count)
	if count != 2 {
		t.Errorf("expected 2 migration records, got %d", count)
	}
}

func TestVerifyPragmas_NoWAL(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Open without WAL pragma to trigger verifyPragmas failure.
	database, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()

	err = verifyPragmas(database)
	if err == nil {
		t.Fatal("expected error when WAL is not enabled")
	}
	if !strings.Contains(err.Error(), "journal_mode") {
		t.Errorf("error = %q, want mention of journal_mode", err.Error())
	}
}

func TestVerifyPragmas_NoFK(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Open with WAL but without FK to trigger the FK check.
	database, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()

	err = verifyPragmas(database)
	if err == nil {
		t.Fatal("expected error when foreign_keys not enabled")
	}
	if !strings.Contains(err.Error(), "foreign_keys") {
		t.Errorf("error = %q, want mention of foreign_keys", err.Error())
	}
}

func TestMigrateFS_ClosedDB(t *testing.T) {
	dir := t.TempDir()
	database, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = database.Close()

	goodFS := fstest.MapFS{
		"m/0001_init.sql": &fstest.MapFile{Data: []byte("CREATE TABLE t (id INTEGER PRIMARY KEY)")},
	}
	err = MigrateFS(database, goodFS, "m")
	if err == nil {
		t.Fatal("expected error when DB is closed")
	}
}

func TestMigrateFS_BadDir(t *testing.T) {
	dir := t.TempDir()
	database, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()

	emptyFS := fstest.MapFS{}
	err = MigrateFS(database, emptyFS, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent migrations directory")
	}
}
