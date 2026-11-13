package fleet

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Apply is a maintenance-only operation: its caller must own the gateway
// supervisor and have waited for the child to exit. DryRun uses the same SQL
// transaction and rolls it back. Unknown schemas are rejected by Export first.
func Apply(ctx context.Context, path string, s Snapshot, dryRun bool) error {
	if err := s.Validate(); err != nil {
		return err
	}
	local, err := Export(ctx, path, s.Epoch)
	if err != nil {
		return fmt.Errorf("inspect destination: %w", err)
	}
	if local.Hash == s.Hash {
		return nil
	}
	db, err := Open(path, false)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") }()
	var settingsJSON string
	if err = conn.QueryRowContext(ctx, "SELECT data FROM settings WHERE id=1").Scan(&settingsJSON); err != nil {
		return err
	}
	settings, err := decode(settingsJSON)
	if err != nil {
		return err
	}
	settings = overlay(settings, s.Settings, sharedSettings)
	settings["autoUpdate"] = false
	b, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "UPDATE settings SET data=? WHERE id=1", string(b)); err != nil {
		return err
	}
	// Insert referenced pools and nodes before their connections. Remove old
	// managed rows after upserting their replacements, preserving local tables.
	for _, table := range []string{"proxyPools", "providerNodes", "providerConnections", "combos"} {
		for _, row := range s.Tables[table] {
			if err = upsert(ctx, conn, table, row, s.Secrets); err != nil {
				return err
			}
		}
		next := map[string]bool{}
		for _, row := range s.Tables[table] {
			next[fmt.Sprint(row["id"])] = true
		}
		for _, row := range local.Tables[table] {
			if !next[fmt.Sprint(row["id"])] {
				if _, err = conn.ExecContext(ctx, "DELETE FROM "+table+" WHERE id=?", row["id"]); err != nil {
					return err
				}
			}
		}
	}
	for _, scope := range sharedScopes {
		if _, err = conn.ExecContext(ctx, "DELETE FROM kv WHERE scope=?", scope); err != nil {
			return err
		}
		for key, value := range s.KV[scope] {
			if _, err = conn.ExecContext(ctx, "INSERT INTO kv(scope,key,value) VALUES(?,?,?)", scope, key, string(value)); err != nil {
				return err
			}
		}
	}
	if dryRun {
		return nil
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	if len(s.Catalog) > 0 {
		return WritePrivate(filepath.Join(filepath.Dir(path), "model-catalog.json"), s.Catalog)
	}
	return nil
}

func upsert(ctx context.Context, db *sql.Conn, table string, row Object, secrets map[string]Object) error {
	cols := append([]string{}, tableColumns[table]...)
	values := make([]any, 0, len(cols)+3)
	for _, k := range cols {
		values = append(values, row[k])
	}
	var created string
	err := db.QueryRowContext(ctx, "SELECT createdAt FROM "+table+" WHERE id=?", row["id"]).Scan(&created)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if created == "" {
		created = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if table != "combos" {
		var raw string
		err := db.QueryRowContext(ctx, "SELECT data FROM "+table+" WHERE id=?", row["id"]).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		before := Object{}
		if raw != "" {
			var err error
			before, err = decode(raw)
			if err != nil {
				return err
			}
		}
		data := overlay(before, object(row["data"]), dataPolicy[table])
		if table == "providerConnections" {
			data["providerSpecificData"] = overlay(object(before["providerSpecificData"]), object(object(row["data"])["providerSpecificData"]), connectionPolicy)
			key, _ := before["apiKey"].(string)
			// Existing sealed credentials are preserved too: never overwrite a sealed
			// slot merely because the plaintext payload is intentionally empty.
			var sealed string
			if err = db.QueryRowContext(ctx, "SELECT COALESCE(secretCiphertext,'') FROM providerConnections WHERE id=?", row["id"]).Scan(&sealed); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if key == "" && sealed == "" && row["authType"] == "apikey" {
				if v, ok := secrets[fmt.Sprint(row["id"])]["apiKey"]; ok {
					data["apiKey"] = v
				}
			}
		}
		b, err := json.Marshal(data)
		if err != nil {
			return err
		}
		cols = append(cols, "data")
		values = append(values, string(b))
	}
	cols = append(cols, "createdAt", "updatedAt")
	values = append(values, created, time.Now().UTC().Format(time.RFC3339Nano))
	placeholders := make([]string, len(cols))
	updates := []string{}
	for i, k := range cols {
		placeholders[i] = "?"
		if k != "id" && k != "createdAt" {
			updates = append(updates, k+"=excluded."+k)
		}
	}
	_, err = db.ExecContext(ctx, "INSERT INTO "+table+"("+strings.Join(cols, ",")+") VALUES("+strings.Join(placeholders, ",")+") ON CONFLICT(id) DO UPDATE SET "+strings.Join(updates, ","), values...)
	return err
}

func overlay(before, next Object, owned []string) Object {
	out := Object{}
	for k, v := range before {
		out[k] = v
	}
	for _, k := range owned {
		delete(out, k)
		v, ok := next[k]
		if !ok {
			continue
		}
		if k == "proxyUrl" || k == "connectionProxyUrl" || k == "baseUrl" {
			old, _ := url.Parse(fmt.Sprint(before[k]))
			u, err := url.Parse(fmt.Sprint(v))
			if err == nil && old != nil && old.User != nil {
				u.User = old.User
				v = u.String()
			}
		}
		out[k] = v
	}
	return out
}

// Backup uses VACUUM INTO: SQLite takes a consistent database snapshot,
// including committed WAL pages. The output must not already exist.
func Backup(ctx context.Context, path, dest string) error {
	db, err := Open(path, true)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "VACUUM INTO ?", dest)
	if err != nil {
		return err
	}
	return os.Chmod(dest, 0600)
}
