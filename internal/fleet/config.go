package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Role string

const (
	Authority Role = "authority"
	Follower  Role = "follower"
)

type DeviceConfig struct {
	Role             Role              `json:"role"`
	Root             string            `json:"root"`
	DataDir          string            `json:"dataDir"`
	Port             int               `json:"port"`
	Host             string            `json:"host"`
	AuthorityURL     string            `json:"authorityUrl"`
	Epoch            string            `json:"epoch"`
	ConfigPublicKey  string            `json:"configPublicKey"`
	ConfigPrivateKey string            `json:"configPrivateKey,omitempty"`
	CodePublicKey    string            `json:"codePublicKey"`
	CodePrivateKey   string            `json:"codePrivateKey,omitempty"`
	GitHubCLI        string            `json:"githubCli,omitempty"`
	CandidateURL     string            `json:"candidateUrl,omitempty"`
	Devices          []string          `json:"devices,omitempty"`
	SecretDevices    []string          `json:"secretDevices,omitempty"`
	SSHHost          string            `json:"sshHost,omitempty"`
	SSHIdentity      string            `json:"sshIdentity,omitempty"`
	SSHKnownHosts    string            `json:"sshKnownHosts,omitempty"`
	InitialBinary    string            `json:"initialBinary,omitempty"`
	Environment      map[string]string `json:"environment,omitempty"`
}

func LoadConfig(path string) (DeviceConfig, error) {
	var c DeviceConfig
	if err := ReadJSON(path, &c); err != nil {
		return c, err
	}
	if c.Role != Authority && c.Role != Follower {
		return c, fmt.Errorf("role must be authority or follower")
	}
	for _, p := range []string{c.Root, c.DataDir} {
		if !filepath.IsAbs(p) {
			return c, fmt.Errorf("root and dataDir must be absolute")
		}
	}
	if c.Port < 1 || c.Port > 65535 || c.Epoch == "" || c.ConfigPublicKey == "" || c.CodePublicKey == "" {
		return c, fmt.Errorf("incomplete device configuration")
	}
	if c.Host == "" {
		c.Host = "127.0.0.1"
	}
	if c.Host != "127.0.0.1" && c.Host != "::1" {
		return c, fmt.Errorf("fleet gateway must bind to loopback; use a separate reverse proxy")
	}
	if c.Role == Follower && (c.SSHHost == "" || c.SSHIdentity == "" || c.SSHKnownHosts == "") {
		return c, fmt.Errorf("followers require SSH host, identity and pinned known-hosts file")
	}
	if c.Role == Authority && (c.ConfigPrivateKey == "" || c.CodePrivateKey == "" || len(c.Devices) == 0) {
		return c, fmt.Errorf("authority requires private signing key and enrolled device allowlist")
	}
	if c.GitHubCLI == "" {
		c.GitHubCLI = "gh"
	}
	if err := os.MkdirAll(c.Root, 0700); err != nil {
		return c, err
	}
	return c, nil
}
func (c DeviceConfig) DBPath() string           { return filepath.Join(c.DataDir, "db", "data.sqlite") }
func (c DeviceConfig) state(name string) string { return filepath.Join(c.Root, name) }
func (c DeviceConfig) GatewayEnvironment() []string {
	overrides := map[string]string{}
	for k, v := range c.Environment {
		overrides[k] = v
	}
	for k, v := range map[string]string{"DATA_DIR": c.DataDir, "DB_PATH": c.DBPath(), "HOST": c.Host, "PORT": fmt.Sprint(c.Port), "AUTO_UPDATE": "false", "FLEET_ROLE": string(c.Role), "FLEET_STATE_DIR": c.Root, "FLEET_AUTHORITY_URL": c.AuthorityURL, "UPDATE_URL": "", "UPDATE_REPO": ""} {
		overrides[k] = v
	}
	out := []string{}
	for _, entry := range os.Environ() {
		k, _, _ := strings.Cut(entry, "=")
		if _, ok := overrides[k]; !ok {
			out = append(out, entry)
		}
	}
	for k, v := range overrides {
		out = append(out, k+"="+v)
	}
	return out
}
