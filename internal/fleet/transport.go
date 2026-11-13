package fleet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, fmt.Errorf("SSH export exceeds size limit")
	}
	return b.Buffer.Write(p)
}
func sshExport(ctx context.Context, c DeviceConfig) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-T", "-F", "none", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "IdentitiesOnly=yes", "-o", "ConnectTimeout=10", "-o", "UserKnownHostsFile="+c.SSHKnownHosts, "-i", c.SSHIdentity, c.SSHHost, "export")
	quietCommand(cmd)
	var out = boundedBuffer{limit: 32 << 20}
	cmd.Stdout = &out
	var stderr = boundedBuffer{limit: 4096}
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("authority SSH export failed: %w", err)
	}
	return out.Bytes(), nil
}
func cliToken(c DeviceConfig) (string, error) {
	machine, err := os.ReadFile(c.DataDir + string(os.PathSeparator) + "machine-id")
	if err != nil {
		return "", err
	}
	secret, err := os.ReadFile(c.DataDir + string(os.PathSeparator) + "auth" + string(os.PathSeparator) + "cli-secret")
	if err != nil {
		return "", err
	}
	h := sha256.Sum256([]byte(strings.TrimSpace(string(machine)) + "9r-cli-auth" + strings.TrimSpace(string(secret))))
	return hex.EncodeToString(h[:])[:16], nil
}
func localGet(ctx context.Context, c DeviceConfig, path string, v any) error {
	uri := "http://" + net.JoinHostPort(c.Host, fmt.Sprint(c.Port)) + path
	req, err := http.NewRequestWithContext(ctx, "GET", uri, nil)
	if err != nil {
		return err
	}
	if token, err := cliToken(c); err == nil {
		req.Header.Set("x-9r-cli-token", token)
	}
	if path == "/v1/models" {
		db, err := Open(c.DBPath(), true)
		if err != nil {
			return err
		}
		var key string
		err = db.QueryRowContext(ctx, "SELECT key FROM apiKeys WHERE isActive=1 AND COALESCE(keyHash,'')='' LIMIT 1").Scan(&key)
		_ = db.Close()
		if err != nil || key == "" {
			return fmt.Errorf("models probation needs a local active plaintext probe key")
		}
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s returned HTTP %d", path, resp.StatusCode)
	}
	if v == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<20))
		return err
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v)
}
