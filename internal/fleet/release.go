package fleet

import (
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Asset struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}
type Release struct {
	Repository string           `json:"repository"`
	Commit     string           `json:"commit"`
	Sequence   uint64           `json:"sequence"`
	Schema     int              `json:"schema"`
	Assets     map[string]Asset `json:"assets"`
}
type Active struct {
	Commit   string `json:"commit"`
	Sequence uint64 `json:"sequence"`
	Gateway  string `json:"gateway"`
	Agent    string `json:"agent,omitempty"`
}

func platform() string { return runtime.GOOS + "-" + runtime.GOARCH }
func executableSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}
func (r Release) Validate() error {
	if r.Repository != Repository || r.Schema != SchemaVersion || len(r.Commit) != 40 {
		return fmt.Errorf("release does not belong to the supported fork/channel")
	}
	if _, err := hex.DecodeString(r.Commit); err != nil {
		return fmt.Errorf("invalid release commit")
	}
	for name, asset := range r.Assets {
		if !strings.HasPrefix(name, "9router-go-") && !strings.HasPrefix(name, "9router-fleet-") {
			return fmt.Errorf("unexpected asset name")
		}
		u, err := url.Parse(asset.URL)
		if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/"+Repository+"/releases/download/fleet-"+r.Commit+"/"+name {
			return fmt.Errorf("artifact URL outside pinned fork release")
		}
		digest, err := hex.DecodeString(asset.SHA256)
		if err != nil || len(digest) != 32 {
			return fmt.Errorf("invalid artifact digest")
		}
	}
	return nil
}
func FetchAttested(ctx context.Context, c DeviceConfig, uri string) (Signed, error) {
	var out Signed
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || !strings.HasPrefix(u.Path, "/"+Repository+"/releases/download/") {
		return out, fmt.Errorf("candidate URL outside pinned fork")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return out, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return out, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("candidate HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return out, err
	}
	var release Release
	if err = json.Unmarshal(raw, &release); err != nil {
		return out, err
	}
	if err = release.Validate(); err != nil {
		return out, err
	}
	path := c.state("candidate-manifest.json")
	if err = WritePrivate(path, raw); err != nil {
		return out, err
	}
	verifyCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(verifyCtx, c.GitHubCLI, "attestation", "verify", path, "--repo", Repository, "--signer-workflow", Repository+"/.github/workflows/fleet.yml", "--source-ref", "refs/heads/main", "--source-digest", release.Commit, "--deny-self-hosted-runners")
	quietCommand(cmd)
	var output = boundedBuffer{limit: 1 << 20}
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err = cmd.Run(); err != nil {
		return out, fmt.Errorf("GitHub build attestation verification failed: %w", err)
	}
	seed, err := os.ReadFile(c.CodePrivateKey)
	if err != nil {
		return out, err
	}
	return Sign(release, strings.TrimSpace(string(seed)))
}
func StageRelease(ctx context.Context, c DeviceConfig, signed Signed, current Active) (Active, error) {
	var r Release
	if err := signed.Verify(c.CodePublicKey, &r); err != nil {
		return current, err
	}
	if err := r.Validate(); err != nil {
		return current, err
	}
	if r.Sequence < current.Sequence {
		return current, fmt.Errorf("refusing stale code release")
	}
	if r.Commit == current.Commit && current.Agent != "" {
		current.Sequence = r.Sequence
		return current, nil
	}
	dir := c.state(filepath.Join("releases", r.Commit))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return current, err
	}
	next := Active{Commit: r.Commit, Sequence: r.Sequence}
	for _, kind := range []string{"9router-go", "9router-fleet"} {
		name := kind + "-" + platform() + executableSuffix()
		asset, ok := r.Assets[name]
		if !ok {
			return current, fmt.Errorf("release lacks %s", platform())
		}
		dest := filepath.Join(dir, kind+executableSuffix())
		if err := downloadAsset(ctx, asset, dest); err != nil {
			return current, err
		}
		info, err := buildinfo.ReadFile(dest)
		if err != nil {
			return current, err
		}
		settings := map[string]string{}
		for _, v := range info.Settings {
			settings[v.Key] = v.Value
		}
		if settings["GOOS"] != runtime.GOOS || settings["GOARCH"] != runtime.GOARCH || settings["vcs.revision"] != r.Commit || settings["vcs.modified"] != "false" {
			return current, fmt.Errorf("binary provenance/architecture mismatch")
		}
		if kind == "9router-go" {
			next.Gateway = dest
		} else {
			next.Agent = dest
		}
	}
	return next, nil
}
func downloadAsset(ctx context.Context, a Asset, dest string) error {
	if h, err := FileDigest(dest); err == nil && h == a.SHA256 {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return fmt.Errorf("artifact HTTP %d", resp.StatusCode)
	}
	f, err := os.CreateTemp(filepath.Dir(dest), ".download-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 150<<20))
	if err == nil && n >= 150<<20 {
		err = fmt.Errorf("artifact exceeds size limit")
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		err = fmt.Errorf("artifact checksum mismatch")
	}
	if err == nil {
		err = f.Chmod(0700)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}
