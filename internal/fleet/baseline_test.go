package fleet

import (
	"context"
	"path/filepath"
	"testing"
)

func TestRollbackRestoresAcknowledgedBaselineWithDatabase(t *testing.T) {
	path, db := fixture(t, "local")
	c := DeviceConfig{Root: t.TempDir(), DataDir: filepath.Dir(filepath.Dir(path))}
	old := SyncStatus{Hash: "old", Revision: 1}
	if err := WriteJSON(c.state("status.json"), old); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(c.state("managed-connections.json"), []string{"account"}); err != nil {
		t.Fatal(err)
	}
	pending, err := prepareBackup(context.Background(), c, Active{Commit: "old"}, Active{Commit: "candidate"})
	if err != nil {
		t.Fatal(err)
	}
	// Emulate a crash after committing the new baseline but before clearing
	// the deployment journal. The recovery point must include both files.
	if err = WriteJSON(c.state("status.json"), SyncStatus{Hash: "candidate", Revision: 2}); err != nil {
		t.Fatal(err)
	}
	if err = WriteJSON(c.state("managed-connections.json"), []string{"candidate"}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE combos SET name='candidate'"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = restore(c, pending); err != nil {
		t.Fatal(err)
	}
	var status SyncStatus
	if err = ReadJSON(c.state("status.json"), &status); err != nil || status.Hash != "old" {
		t.Fatal("baseline and restored database diverged", err)
	}
	var managed []string
	if err = ReadJSON(c.state("managed-connections.json"), &managed); err != nil || len(managed) != 1 || managed[0] != "account" {
		t.Fatal("managed ownership was not restored", err)
	}
}
