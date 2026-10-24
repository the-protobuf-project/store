// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package references_test

// db_test.go opens a fresh Postgres database per test, on the server
// STORE_TEST_POSTGRES_DSN names (a throwaway one: the tests create and drop
// databases). Without it the database tests skip.
//
//	STORE_TEST_POSTGRES_DSN="host=localhost port=55432 user=postgres dbname=postgres sslmode=disable" go test ./references/

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const dsnEnv = "STORE_TEST_POSTGRES_DSN"

// freshDB creates an empty database, returns a connection to it, and drops it
// when the test ends.
func freshDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s not set; skipping the Postgres tests", dsnEnv)
	}
	cfg := &gorm.Config{Logger: logger.Discard}
	admin, err := gorm.Open(postgres.Open(dsn), cfg)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("store_refs_%d", time.Now().UnixNano())
	if err := admin.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(postgres.Open(withDBName(dsn, name)), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sql, err := db.DB(); err == nil {
			sql.Close()
		}
		admin.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)")
		if sql, err := admin.DB(); err == nil {
			sql.Close()
		}
	})
	return db
}

// withDBName replaces (or adds) dbname in a key=value DSN.
func withDBName(dsn, name string) string {
	var parts []string
	for _, p := range strings.Fields(dsn) {
		if !strings.HasPrefix(p, "dbname=") {
			parts = append(parts, p)
		}
	}
	return strings.Join(append(parts, "dbname="+name), " ")
}
