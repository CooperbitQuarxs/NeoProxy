package neoroute

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
)

func assertIndependentVersions(t *testing.T, identity *neo.Store, routing int) {
	t.Helper()
	for query, want := range map[string]int{
		"PRAGMA user_version":                 1,
		"SELECT version FROM neo_pool_schema": 1,
		"SELECT version FROM neoroute_schema": routing,
	} {
		var got int
		if err := identity.DB().QueryRowContext(t.Context(), query).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: version=%d want=%d error=%v", query, got, want, err)
		}
	}
}

func TestRoutingMigrationReopensAndAdoptsUnversionedData(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "versioned", true: "legacy-unversioned"}[legacy], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.sqlite")
			identity, err := neo.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if identity != nil {
					if err := identity.Close(); err != nil {
						t.Error(err)
					}
				}
			}()
			pool, err := neopool.New(identity.DB(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = New(identity, pool); err != nil {
				t.Fatal(err)
			}
			assertIndependentVersions(t, identity, 1)
			if _, err = identity.DB().ExecContext(t.Context(), `INSERT INTO neoroute_admissions(id,user_id,at,day,tokens) VALUES('retained','user',1,0,17)`); err != nil {
				t.Fatal(err)
			}
			if legacy {
				if _, err = identity.DB().ExecContext(t.Context(), `DROP TABLE neoroute_schema`); err != nil {
					t.Fatal(err)
				}
			}
			if err = identity.Close(); err != nil {
				t.Fatal(err)
			}
			identity, err = neo.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			pool, err = neopool.New(identity.DB(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = New(identity, pool); err != nil {
				t.Fatal(err)
			}
			assertIndependentVersions(t, identity, 1)
			var tokens int64
			if err = identity.DB().QueryRowContext(t.Context(), `SELECT tokens FROM neoroute_admissions WHERE id='retained'`).Scan(&tokens); err != nil || tokens != 17 {
				t.Fatalf("migration changed persisted admissions: tokens=%d error=%v", tokens, err)
			}
		})
	}
}

func TestRoutingMigrationRejectsFutureVersionWithoutDDL(t *testing.T) {
	identity, err := neo.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := identity.Close(); err != nil {
			t.Error(err)
		}
	}()
	pool, err := neopool.New(identity.DB(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = identity.DB().ExecContext(t.Context(), `CREATE TABLE neoroute_schema(version INTEGER NOT NULL); INSERT INTO neoroute_schema VALUES(99)`); err != nil {
		t.Fatal(err)
	}
	if _, err = New(identity, pool); err == nil || !strings.Contains(err.Error(), "unsupported routing schema version 99") {
		t.Fatalf("future routing version accepted: %v", err)
	}
	assertIndependentVersions(t, identity, 99)
	var tables int
	if err = identity.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='neoroute_virtual_models'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("future-version rejection changed schema: tables=%d error=%v", tables, err)
	}
}

func TestRoutingMigrationRollsBackInitialSchemaOnDDLFailure(t *testing.T) {
	identity, err := neo.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := identity.Close(); err != nil {
			t.Error(err)
		}
	}()
	pool, err := neopool.New(identity.DB(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// The incompatible pre-existing requests table makes index creation fail,
	// after earlier routing DDL has executed inside the same transaction.
	if _, err = identity.DB().ExecContext(t.Context(), `CREATE TABLE neoroute_requests(id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err = New(identity, pool); err == nil {
		t.Fatal("incompatible schema was accepted")
	}
	var tables int
	if err = identity.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('neoroute_schema','neoroute_virtual_models','neoroute_cursors')`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("failed migration left partial routing schema: tables=%d error=%v", tables, err)
	}
	if _, err = identity.DB().ExecContext(t.Context(), `DROP TABLE neoroute_requests`); err != nil {
		t.Fatal(err)
	}
	if _, err = New(identity, pool); err != nil {
		t.Fatal(err)
	}
	assertIndependentVersions(t, identity, 1)
}
