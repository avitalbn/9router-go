package fleet

import (
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"time"
)

var ErrAgentUpdated = errors.New("fleet helper updated; supervisor should relaunch")
var ErrRolledBack = errors.New("candidate rolled back")

type SyncStatus struct {
	Role           Role   `json:"role"`
	Commit         string `json:"commit"`
	ForkHead       string `json:"forkHead,omitempty"`
	Epoch          string `json:"epoch"`
	Revision       uint64 `json:"revision"`
	Hash           string `json:"hash"`
	LastCheck      string `json:"lastCheck"`
	LastSuccess    string `json:"lastSuccess"`
	LastError      string `json:"lastError,omitempty"`
	CodeError      string `json:"codeError,omitempty"`
	FailedRevision string `json:"failedRevision,omitempty"`
}
type agent struct {
	config   DeviceConfig
	active   Active
	process  *child
	status   SyncStatus
	lastCode time.Time
}

func netDial(ctx context.Context, c DeviceConfig) (net.Conn, error) {
	return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(c.Host, fmt.Sprint(c.Port)))
}
func initialActive(c DeviceConfig) (Active, error) {
	var a Active
	if err := ReadJSON(c.state("active.json"), &a); err == nil {
		return a, nil
	} else if !os.IsNotExist(err) {
		return a, err
	}
	if c.InitialBinary == "" {
		return a, fmt.Errorf("enroll with an initial gateway binary")
	}
	info, err := buildinfo.ReadFile(c.InitialBinary)
	if err != nil {
		return a, err
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			a.Commit = s.Value
		}
	}
	if a.Commit == "" {
		return a, fmt.Errorf("initial binary has no source revision")
	}
	a.Gateway = c.InitialBinary
	return a, nil
}
func Run(ctx context.Context, c DeviceConfig) error {
	unlock, err := Lock(c.state("agent.lock"))
	if err != nil {
		return fmt.Errorf("another fleet supervisor owns this device: %w", err)
	}
	defer unlock()
	if err = recoverDeployment(ctx, c); err != nil {
		return fmt.Errorf("recover deployment: %w", err)
	}
	a, err := initialActive(c)
	if err != nil {
		return err
	}
	if conn, err := netDial(ctx, c); err == nil {
		_ = conn.Close()
		return fmt.Errorf("port is already occupied; stop the previous gateway supervisor before enrollment")
	}
	g := &agent{config: c, active: a}
	_ = ReadJSON(c.state("status.json"), &g.status)
	g.status.Role = c.Role
	g.status.Epoch = c.Epoch
	g.status.Commit = a.Commit
	if g.process, err = startChild(c, a); err != nil {
		return err
	}
	defer func() { _ = stopChild(c, g.process) }()
	if err = waitHealthy(ctx, c, a); err != nil {
		return err
	}
	if err = os.Remove(c.state("probation")); err != nil {
		return err
	}
	if err = WriteJSON(c.state("active.json"), a); err != nil {
		return err
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-g.process.done:
			return fmt.Errorf("gateway child exited unexpectedly: %w", err)
		case <-timer.C:
			if err = g.sync(ctx); err != nil {
				if errors.Is(err, ErrAgentUpdated) {
					return err
				}
				g.status.LastError = err.Error()
				log.Printf("fleet: %v", err)
			} else {
				g.status.LastError = ""
			}
			_ = WriteJSON(c.state("status.json"), g.status)
			if g.process == nil {
				return fmt.Errorf("gateway could not be restarted; recovery requires supervisor restart")
			}
			timer.Reset(time.Duration(55+rand.IntN(11)) * time.Second)
		}
	}
}
func (g *agent) sync(ctx context.Context) error {
	c := g.config
	g.status.LastCheck = time.Now().UTC().Format(time.RFC3339)
	if err := g.applyQueuedBinding(ctx); err != nil {
		return err
	}
	bundle, s, err := Receive(ctx, c)
	if err != nil {
		return err
	}
	if c.Role == Follower {
		g.status.ForkHead = bundle.ForkHead
		g.status.CodeError = bundle.CodeError
	}
	if s.Revision < g.status.Revision {
		return fmt.Errorf("refusing stale configuration revision")
	}
	local, err := Export(ctx, c.DBPath(), c.Epoch)
	if err != nil {
		return err
	}
	if c.Role == Follower && g.status.Hash != "" && local.Hash != g.status.Hash {
		return fmt.Errorf("local managed configuration drift; resolve on authority or explicitly re-enroll")
	}
	next := g.active
	code := bundle.Release
	if c.Role == Authority && time.Since(g.lastCode) > 5*time.Minute && c.CandidateURL != "" {
		g.lastCode = time.Now()
		head, headErr := forkHead(ctx)
		if headErr == nil {
			g.status.ForkHead = head
		}
		candidate, fetchErr := FetchAttested(ctx, c, c.CandidateURL)
		if fetchErr != nil {
			g.status.CodeError = fetchErr.Error()
		} else {
			code = &candidate
			g.status.CodeError = ""
			var candidateRelease Release
			if err := json.Unmarshal(candidate.Payload, &candidateRelease); err != nil {
				return err
			}
			if headErr != nil {
				g.status.CodeError = headErr.Error()
			} else if candidateRelease.Commit != head {
				g.status.CodeError = "Latest fork HEAD is awaiting a successful fleet build and canary"
			}
		}
	}
	if code != nil {
		next, err = StageRelease(ctx, c, *code, g.active)
		if err != nil {
			return err
		}
	}
	if next.Commit == g.active.Commit && next.Gateway == g.active.Gateway && (c.Role == Authority || local.Hash == s.Hash) {
		g.active = next
		if err = WriteJSON(c.state("active.json"), next); err != nil {
			return err
		}
		if c.Role == Authority && code != nil {
			if err = WriteJSON(c.state("approved-release.json"), code); err != nil {
				return err
			}
		}
		return g.acknowledge(s)
	}
	revision := fmt.Sprintf("%s/%d", next.Commit, s.Revision)
	if g.status.FailedRevision == revision {
		return fmt.Errorf("deployment previously failed probation; inspect logs and clear failedRevision to retry")
	}
	if err = g.deploy(ctx, next, s); err != nil {
		if errors.Is(err, ErrRolledBack) {
			g.status.FailedRevision = revision
		}
		return err
	}
	if c.Role == Authority && code != nil {
		if err = WriteJSON(c.state("approved-release.json"), code); err != nil {
			return err
		}
	}
	if err = g.acknowledge(s); err != nil {
		return err
	}
	if next.Agent != "" {
		exe, _ := os.Executable()
		if filepath.Clean(exe) != filepath.Clean(next.Agent) {
			return ErrAgentUpdated
		}
	}
	return nil
}
func (g *agent) acknowledge(s Snapshot) error {
	ids := []string{}
	for _, row := range s.Tables["providerConnections"] {
		ids = append(ids, fmt.Sprint(row["id"]))
	}
	if err := WriteJSON(g.config.state("managed-connections.json"), ids); err != nil {
		return err
	}
	g.status.Revision = s.Revision
	g.status.Hash = s.Hash
	g.status.Commit = g.active.Commit
	g.status.LastSuccess = time.Now().UTC().Format(time.RFC3339)
	g.status.FailedRevision = ""
	return WriteJSON(g.config.state("status.json"), g.status)
}
