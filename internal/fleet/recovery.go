package fleet

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Pending struct {
	Before    Active `json:"before"`
	After     Active `json:"after"`
	BackupDir string `json:"backupDir"`
}

func prepareBackup(ctx context.Context, c DeviceConfig, before, after Active) (Pending, error) {
	dir := c.state(filepath.Join("backups", time.Now().UTC().Format("20060102T150405.000000000")))
	p := Pending{Before: before, After: after, BackupDir: dir}
	if err := Backup(ctx, c.DBPath(), filepath.Join(dir, "database.sqlite")); err != nil {
		return p, err
	}
	catalog := filepath.Join(filepath.Dir(c.DBPath()), "model-catalog.json")
	if err := copyPrivate(catalog, filepath.Join(dir, "model-catalog.json")); err != nil && !os.IsNotExist(err) {
		return p, err
	}
	if err := WriteJSON(filepath.Join(dir, "active.json"), before); err != nil {
		return p, err
	}
	return p, WriteJSON(c.state("pending.json"), p)
}
func restore(c DeviceConfig, p Pending) error {
	// Caller has stopped every gateway writer. Restore a checkpointed snapshot,
	// never a standalone file with unrelated WAL pages still beside it.
	failed := filepath.Join(p.BackupDir, "failed-database.sqlite")
	if _, err := os.Stat(failed); os.IsNotExist(err) {
		_ = Backup(context.Background(), c.DBPath(), failed)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if err := os.Remove(c.DBPath() + suffix); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := copyPrivate(filepath.Join(p.BackupDir, "database.sqlite"), c.DBPath()); err != nil {
		return err
	}
	dest := filepath.Join(filepath.Dir(c.DBPath()), "model-catalog.json")
	if err := copyPrivate(filepath.Join(p.BackupDir, "model-catalog.json"), dest); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if err = os.Remove(dest); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := WriteJSON(c.state("active.json"), p.Before); err != nil {
		return err
	}
	return os.Remove(c.state("pending.json"))
}
func recoverDeployment(ctx context.Context, c DeviceConfig) error {
	var pending Pending
	if err := ReadJSON(c.state("pending.json"), &pending); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	// An orphan from a supervisor crash may still be running. Only stop a
	// gateway authenticating with this data directory and reporting fleet mode.
	var status struct {
		Role string `json:"role"`
	}
	if localGet(ctx, c, "/api/fleet/status", &status) == nil {
		if status.Role != string(c.Role) {
			return fmt.Errorf("unexpected listener blocks recovery")
		}
		if err := shutdownGateway(ctx, c); err != nil {
			return err
		}
		for i := 0; i < 20; i++ {
			conn, err := netDial(ctx, c)
			if err != nil {
				break
			}
			_ = conn.Close()
			if i == 19 {
				return fmt.Errorf("gateway still listening during recovery")
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	return restore(c, pending)
}
