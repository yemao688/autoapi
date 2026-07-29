package store

import (
	"database/sql"
	"testing"
)

func migrationsThrough(id string) []migration {
	for i, m := range migrations {
		if m.ID == id {
			return migrations[:i+1]
		}
	}
	return nil
}

func TestRequestPriceMigrationsFrom020DropAndIdempotence(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := applyMigrations(db, migrationsThrough("020_model_rule_strategy")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO providers(id,name,base_url,created_at,updated_at) VALUES('p','p','http://p',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO models(id,provider_id,name,created_at) VALUES('m','p','m',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO prices(id,upstream_model,updated_at,created_at) VALUES('price','m',1,1)`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	var price float64
	if err := db.QueryRow(`SELECT request_price FROM models WHERE id='m'`).Scan(&price); err != nil || price != .1 {
		t.Fatalf("default=%v err=%v", price, err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='prices'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("prices table count=%d err=%v", n, err)
	}
	if _, err := db.Exec(`UPDATE models SET request_price=-1 WHERE id='m'`); err == nil {
		t.Fatal("negative price accepted")
	}
	if _, err := db.Exec(`UPDATE models SET request_price='NaN' WHERE id='m'`); err == nil {
		t.Fatal("NaN price accepted")
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
}

func TestRequestPriceMigrationSkipIfRedundantPreservesZero(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := applyMigrations(db, migrationsThrough("020_model_rule_strategy")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE models ADD COLUMN request_price REAL NOT NULL DEFAULT 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO providers(id,name,base_url,created_at,updated_at) VALUES('p','p','http://p',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO models(id,provider_id,name,request_price,created_at) VALUES('m','p','m',0,1)`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	var price float64
	if err := db.QueryRow(`SELECT request_price FROM models WHERE id='m'`).Scan(&price); err != nil || price != 0 {
		t.Fatalf("explicit zero=%v err=%v", price, err)
	}
	if _, err := db.Exec(`UPDATE models SET request_price=-1 WHERE id='m'`); err == nil {
		t.Fatal("023 trigger missing after redundant 021")
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
}

func TestOmoSlimResourceMigrationRenamesLegacyState(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := applyMigrations(db, migrationsThrough("037_tool_access_tables")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tool_file_state(tool, resource, path) VALUES('opencode', 'opencode-omo', '/tmp/omo.jsonc')`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}

	var resource string
	if err := db.QueryRow(`SELECT resource FROM tool_file_state WHERE tool='opencode'`).Scan(&resource); err != nil {
		t.Fatal(err)
	}
	if resource != "opencode-omo-slim" {
		t.Fatalf("migrated resource = %q", resource)
	}
}

func TestOmoSlimResourceMigrationSkipsWithoutLegacyState(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := applyMigrations(db, migrationsThrough("037_tool_access_tables")); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM _migrations WHERE id='038_rename_omo_slim_resource'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("migration record count = %d, want 1", count)
	}
}
