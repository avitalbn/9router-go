package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

type CredentialBinding struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

var credentialFields = words("apiKey accessToken refreshToken idToken authToken cookie sessionToken expiresAt expiresIn tokenType scope projectId")
var sealedFields = words("secretWrappedDEK secretCiphertext tokenWrappedDEK tokenCiphertext refreshWrappedDEK refreshCiphertext")

// QueueBinding requests a local maintenance operation, not a credential export.
// Only an extra local login row can be moved onto a fleet-owned definition.
func QueueBinding(c DeviceConfig, b CredentialBinding) error {
	if c.Role != Follower || b.Source == "" || b.Target == "" || b.Source == b.Target {
		return fmt.Errorf("binding requires a follower and distinct source/target IDs")
	}
	var managed []string
	if err := ReadJSON(c.state("managed-connections.json"), &managed); err != nil {
		return err
	}
	if contains(managed, b.Source) || !contains(managed, b.Target) {
		return fmt.Errorf("source must be an extra local login; target must be fleet-managed")
	}
	return WriteJSON(c.state("credential-request.json"), b)
}
func (g *agent) applyQueuedBinding(ctx context.Context) error {
	c := g.config
	var b CredentialBinding
	if err := ReadJSON(c.state("credential-request.json"), &b); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err := QueueBinding(c, b); err != nil {
		return err
	}
	if g.status.Hash == "" {
		return fmt.Errorf("receive the initial shared configuration before binding credentials")
	}
	expected := Snapshot{Hash: g.status.Hash}
	if err := g.deployWith(ctx, g.active, expected, func() error { return bindCredentials(ctx, c.DBPath(), b) }); err != nil {
		return err
	}
	return os.Rename(c.state("credential-request.json"), c.state("credentials-bound.json"))
}
func bindCredentials(ctx context.Context, path string, b CredentialBinding) error {
	db, err := Open(path, false)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := readRows(ctx, tx, "SELECT * FROM providerConnections WHERE id IN (?,?) ORDER BY id", b.Source, b.Target)
	if err != nil {
		return err
	}
	var source, target Object
	for _, row := range rows {
		if row["id"] == b.Source {
			source = row
		}
		if row["id"] == b.Target {
			target = row
		}
	}
	if source == nil || target == nil {
		return fmt.Errorf("credential binding connection not found")
	}
	if source["provider"] != target["provider"] || source["authType"] != target["authType"] {
		return fmt.Errorf("credential binding cannot cross provider or authentication boundaries")
	}
	from, err := decode(fmt.Sprint(source["data"]))
	if err != nil {
		return err
	}
	to, err := decode(fmt.Sprint(target["data"]))
	if err != nil {
		return err
	}
	to = overlay(to, pick(from, credentialFields), credentialFields)
	to["providerSpecificData"] = overlay(object(to["providerSpecificData"]), pick(object(from["providerSpecificData"]), localAccountFields), localAccountFields)
	delete(to, "oauthLockedUntil")
	to["oauthFailureCount"] = 0
	to["testStatus"] = "active"
	data, err := json.Marshal(to)
	if err != nil {
		return err
	}
	columns := []string{"data=?", "email=?", "updatedAt=?"}
	args := []any{string(data), source["email"], time.Now().UTC().Format(time.RFC3339Nano)}
	for _, column := range sealedFields {
		columns = append(columns, column+"=?")
		args = append(args, source[column])
	}
	args = append(args, b.Target)
	if _, err = tx.ExecContext(ctx, "UPDATE providerConnections SET "+strings.Join(columns, ",")+" WHERE id=?", args...); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM providerConnections WHERE id=?", b.Source); err != nil {
		return err
	}
	return tx.Commit()
}
