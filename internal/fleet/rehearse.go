package fleet

import (
	"context"
	"fmt"
	"os"
)

// Rehearse copies a local database to a new private destination, applies the
// supplied policy there, and verifies local credentials/authorization survived.
// Neither database nor credentials are sent anywhere by this operation.
func Rehearse(ctx context.Context, source, destination string, s Snapshot) error {
	if _, err := os.Stat(destination); err == nil {
		return fmt.Errorf("rehearsal output already exists")
	}
	if err := Backup(ctx, source, destination); err != nil {
		return err
	}
	before, err := privateFingerprint(ctx, destination)
	if err != nil {
		return err
	}
	if err = Apply(ctx, destination, s, false); err != nil {
		return err
	}
	after, err := privateFingerprint(ctx, destination)
	if err != nil {
		return err
	}
	if before != after {
		return fmt.Errorf("rehearsal changed local credentials, authorization or runtime state")
	}
	applied, err := Export(ctx, destination, s.Epoch)
	if err != nil {
		return err
	}
	if applied.Hash != s.Hash {
		return fmt.Errorf("rehearsal did not converge")
	}
	return nil
}
func privateFingerprint(ctx context.Context, path string) (string, error) {
	db, err := Open(path, true)
	if err != nil {
		return "", err
	}
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	result := Object{}
	var raw string
	if err = tx.QueryRowContext(ctx, "SELECT data FROM settings WHERE id=1").Scan(&raw); err != nil {
		return "", err
	}
	settings, err := decode(raw)
	if err != nil {
		return "", err
	}
	delete(settings, "autoUpdate")
	for _, key := range sharedSettings {
		delete(settings, key)
	}
	result["settings"] = settings
	rows, err := readRows(ctx, tx, "SELECT * FROM providerConnections ORDER BY id")
	if err != nil {
		return "", err
	}
	for _, row := range rows {
		delete(row, "updatedAt")
		for _, key := range tableColumns["providerConnections"] {
			if key != "id" {
				delete(row, key)
			}
		}
		data, err := decode(fmt.Sprint(row["data"]))
		if err != nil {
			return "", err
		}
		nested := object(data["providerSpecificData"])
		for _, key := range connectionPolicy {
			delete(nested, key)
		}
		for _, key := range connectionPolicy {
			delete(data, key)
		}
		data["providerSpecificData"] = nested
		row["data"] = data
	}
	result["credentialsAndRuntime"] = rows
	keys, err := readRows(ctx, tx, "SELECT * FROM apiKeys ORDER BY id")
	if err != nil {
		return "", err
	}
	result["apiKeys"] = keys
	var count int64
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM usageHistory").Scan(&count); err != nil {
		return "", err
	}
	result["usageRows"] = count
	return digest(result)
}
