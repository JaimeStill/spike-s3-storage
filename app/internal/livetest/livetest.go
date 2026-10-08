//go:build integration

// Package livetest is the helper the integration-tagged tests share: it
// gives each test its own throwaway database on the Postgres server the
// BLOBFS_DATABASE_* variables name, so no test depends on the state of the
// compose stack's database or on another test, and drops the database when
// the test ends. `mise run integration` sets those variables to its
// isolated compose project.
package livetest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"testing"
	"time"

	"github.com/standards-lab/go-database"
	"github.com/standards-lab/go-database/postgres"
)

// envPrefix is the prefix blobfs reads its configuration under, so the
// helper reaches the server the binary does.
const envPrefix = "BLOBFS"

// Database creates a uniquely named database on the server the
// BLOBFS_DATABASE_* variables name and returns its name, which a run of
// the binary takes as BLOBFS_DATABASE_NAME, and a pool connected to it, for
// the test's own reads and writes. The pool is closed and the database
// dropped, with FORCE so open sessions do not block the drop, when the test
// ends. An unreachable server fails the test: the integration tag states
// that the stack is expected.
func Database(t testing.TB) (string, *sql.DB) {
	t.Helper()
	admin := open(t, "")
	name := "blobfs_test_" + suffix(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}
	t.Cleanup(func() {
		// WITH (FORCE) terminates the database's other sessions, and under
		// load the engine can report that one is still closing. Each attempt
		// gets its own deadline, so one long stall does not use up the
		// retries.
		var err error
		for range 4 {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_, err = admin.ExecContext(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
			cancel()
			if err == nil {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Errorf("drop database %s: %v", name, err)
	})
	return name, open(t, name)
}

// open starts a pool on the configured server, connected to the database
// name, or to the configured database when name is empty, and closes it
// when the test ends. Cleanups run last first, so a pool opened after the
// admin pool closes before the drop runs on it.
func open(t testing.TB, name string) *sql.DB {
	t.Helper()
	var cfg database.Config
	if err := cfg.Finalize(envPrefix); err != nil {
		t.Fatalf("database config: %v", err)
	}
	if name != "" {
		// The environment names the stack's own database; the test's
		// throwaway database replaces it.
		cfg.Name = name
	}
	db, err := postgres.New(cfg)
	if err != nil {
		t.Fatalf("database: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.Start(ctx); err != nil {
		t.Fatalf("start database %s: %v", cfg.Name, err)
	}
	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })
	return db.Conn()
}

// suffix returns eight random hex characters, so parallel packages never
// pick the same database name.
func suffix(t testing.TB) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("random suffix: %v", err)
	}
	return hex.EncodeToString(b[:])
}
