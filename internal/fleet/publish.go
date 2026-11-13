package fleet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type Bundle struct {
	Snapshot  Signed  `json:"snapshot"`
	Release   *Signed `json:"release,omitempty"`
	ForkHead  string  `json:"forkHead,omitempty"`
	CodeError string  `json:"codeError,omitempty"`
}

func Publish(ctx context.Context, c DeviceConfig, device string) (Bundle, error) {
	if c.Role != Authority || !contains(c.Devices, device) {
		return Bundle{}, fmt.Errorf("device is not enrolled for export")
	}
	unlock, err := Lock(c.state("publisher.lock"))
	if err != nil {
		return Bundle{}, err
	}
	defer unlock()
	if _, err = os.Stat(c.state("pending.json")); err == nil {
		return Bundle{}, fmt.Errorf("authority deployment in progress; retry")
	}
	s, err := Export(ctx, c.DBPath(), c.Epoch)
	if err != nil {
		return Bundle{}, err
	}
	// The private publisher record includes manual-key changes in revisioning,
	// while the common policy hash deliberately excludes credentials.
	signatureHash, err := digest(s)
	if err != nil {
		return Bundle{}, err
	}
	var previous struct {
		Content  string `json:"content"`
		Revision uint64 `json:"revision"`
	}
	if err = ReadJSON(c.state("publisher.json"), &previous); err != nil && !os.IsNotExist(err) {
		return Bundle{}, err
	}
	if previous.Content != signatureHash {
		previous.Content = signatureHash
		previous.Revision++
		if err = WriteJSON(c.state("publisher.json"), previous); err != nil {
			return Bundle{}, err
		}
	}
	s.Revision = previous.Revision
	// Credential transfer is opt-in per recipient, after global revisioning.
	if !contains(c.SecretDevices, device) {
		s.Secrets = nil
	}
	seed, err := os.ReadFile(c.ConfigPrivateKey)
	if err != nil {
		return Bundle{}, err
	}
	signed, err := Sign(s, strings.TrimSpace(string(seed)))
	if err != nil {
		return Bundle{}, err
	}
	result := Bundle{Snapshot: signed}
	var status SyncStatus
	if ReadJSON(c.state("status.json"), &status) == nil {
		result.ForkHead = status.ForkHead
		result.CodeError = status.CodeError
	}
	var release Signed
	if err = ReadJSON(c.state("approved-release.json"), &release); err == nil {
		result.Release = &release
	} else if !os.IsNotExist(err) {
		return Bundle{}, err
	}
	return result, nil
}

func Receive(ctx context.Context, c DeviceConfig) (Bundle, Snapshot, error) {
	var b Bundle
	if c.Role == Authority {
		var err error
		b, err = Publish(ctx, c, c.Devices[0])
		if err != nil {
			return b, Snapshot{}, err
		}
	} else {
		raw, err := sshExport(ctx, c)
		if err != nil {
			return b, Snapshot{}, err
		}
		if err = json.Unmarshal(raw, &b); err != nil {
			return b, Snapshot{}, fmt.Errorf("invalid authority export")
		}
	}
	var s Snapshot
	if err := b.Snapshot.Verify(c.ConfigPublicKey, &s); err != nil {
		return b, s, err
	}
	if s.Epoch != c.Epoch {
		return b, s, fmt.Errorf("authority epoch mismatch")
	}
	return b, s, s.Validate()
}

func FileDigest(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
