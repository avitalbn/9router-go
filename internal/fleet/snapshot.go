package fleet

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

func Open(path string, readOnly bool) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	// A single connection pins transaction and pragma semantics. Do not bootstrap
	// schema in an exporter: discovering an unknown schema must be read-only.
	dsn := filepath.ToSlash(path)
	if readOnly {
		dsn += "?mode=ro"
	} else {
		dsn += "?mode=rw"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func Export(ctx context.Context, path, epoch string) (Snapshot, error) {
	out := Snapshot{Schema: SchemaVersion, Epoch: epoch, Settings: Object{}, Tables: map[string][]Object{}, KV: map[string]map[string]json.RawMessage{}, Secrets: map[string]Object{}}
	db, err := Open(path, true)
	if err != nil {
		return out, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var raw string
	if err = tx.QueryRowContext(ctx, "SELECT data FROM settings WHERE id=1").Scan(&raw); err != nil {
		return out, err
	}
	settings, err := decode(raw)
	if err != nil {
		return out, err
	}
	for k := range settings {
		if !contains(sharedSettings, k) && !contains(localSettings, k) {
			return out, fmt.Errorf("unclassified setting %s", k)
		}
	}
	out.Settings = pick(settings, sharedSettings)
	for _, table := range sortedTables() {
		cols := append([]string{}, tableColumns[table]...)
		if table != "combos" {
			cols = append(cols, "data")
		}
		rows, err := readRows(ctx, tx, "SELECT "+strings.Join(cols, ",")+" FROM "+table+" ORDER BY id")
		if err != nil {
			return out, fmt.Errorf("export %s: %w", table, err)
		}
		for _, row := range rows {
			if table == "combos" {
				continue
			}
			data, err := decode(fmt.Sprint(row["data"]))
			if err != nil {
				return out, err
			}
			if table == "providerConnections" {
				// Only manual static API keys travel, and apply only fills a missing key.
				// OAuth and session credentials never enter the wire payload.
				if row["authType"] == "apikey" {
					if key, ok := data["apiKey"].(string); ok && key != "" {
						out.Secrets[fmt.Sprint(row["id"])] = Object{"apiKey": key}
					}
				}
			}
			row["data"], err = policyData(table, data)
			if err != nil {
				return out, err
			}
		}
		out.Tables[table] = rows
	}
	for _, scope := range sharedScopes {
		rows, err := tx.QueryContext(ctx, "SELECT key,value FROM kv WHERE scope=? ORDER BY key", scope)
		if err != nil {
			return out, err
		}
		out.KV[scope] = map[string]json.RawMessage{}
		for rows.Next() {
			var k, v string
			if err = rows.Scan(&k, &v); err != nil {
				rows.Close()
				return out, err
			}
			if !json.Valid([]byte(v)) {
				rows.Close()
				return out, fmt.Errorf("invalid JSON in %s", scope)
			}
			out.KV[scope][k] = json.RawMessage(v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	catalog, err := os.ReadFile(filepath.Join(filepath.Dir(path), "model-catalog.json"))
	if err == nil {
		if !json.Valid(catalog) {
			return out, fmt.Errorf("invalid capability catalog")
		}
		out.Catalog = catalog
	} else if !os.IsNotExist(err) {
		return out, err
	}
	out.Hash, err = out.PolicyHash()
	if err != nil {
		return out, err
	}
	return out, out.Validate()
}

func readRows(ctx context.Context, tx *sql.Tx, query string) ([]Object, error) {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []Object{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err = rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := Object{}
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				vals[i] = string(b)
			}
			row[c] = vals[i]
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
