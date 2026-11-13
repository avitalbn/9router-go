package fleet

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"time"
)

type child struct {
	cmd  *exec.Cmd
	done chan error
	log  *os.File
}

func startChild(c DeviceConfig, a Active) (*child, error) {
	if err := WritePrivate(c.state("probation"), []byte("validating")); err != nil {
		return nil, err
	}
	if f, err := os.Stat(c.state("gateway.log")); err == nil && f.Size() > 10<<20 {
		_ = os.Remove(c.state("gateway.previous.log"))
		_ = os.Rename(c.state("gateway.log"), c.state("gateway.previous.log"))
	}
	log, err := os.OpenFile(c.state("gateway.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(a.Gateway)
	quietCommand(cmd)
	cmd.Dir = c.DataDir
	cmd.Env = c.GatewayEnvironment()
	cmd.Stdout = log
	cmd.Stderr = log
	if err = cmd.Start(); err != nil {
		log.Close()
		return nil, err
	}
	out := &child{cmd: cmd, done: make(chan error, 1), log: log}
	go func() { out.done <- cmd.Wait(); close(out.done); _ = log.Close() }()
	return out, nil
}
func waitHealthy(ctx context.Context, c DeviceConfig, a Active) error {
	deadline := time.NewTimer(60 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		var health struct {
			Status string `json:"status"`
		}
		var status struct {
			RunningCommit string `json:"runningCommit"`
			Role          string `json:"role"`
		}
		if localGet(ctx, c, "/health", &health) == nil && health.Status == "ok" && localGet(ctx, c, "/api/fleet/status", &status) == nil && status.RunningCommit == a.Commit && status.Role == string(c.Role) {
			if err := localGet(ctx, c, "/api/settings", &map[string]any{}); err == nil {
				var models struct {
					Data []any `json:"data"`
				}
				if err = localGet(ctx, c, "/", nil); err == nil && localGet(ctx, c, "/v1/models", &models) == nil && models.Data != nil {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("gateway probation failed; inspect private gateway.log")
		case <-tick.C:
		}
	}
}
func shutdownGateway(ctx context.Context, c DeviceConfig) error {
	uri := "http://" + net.JoinHostPort(c.Host, fmt.Sprint(c.Port)) + "/api/version/shutdown"
	req, err := http.NewRequestWithContext(ctx, "POST", uri, nil)
	if err != nil {
		return err
	}
	token, err := cliToken(c)
	if err != nil {
		return err
	}
	req.Header.Set("x-9r-cli-token", token)
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("shutdown HTTP %d", resp.StatusCode)
	}
	return nil
}
func stopChild(c DeviceConfig, p *child) error {
	if p == nil {
		return nil
	}
	select {
	case <-p.done:
		return nil
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = shutdownGateway(ctx, c)
	cancel()
	select {
	case <-p.done:
		return nil
	case <-time.After(15 * time.Second):
		// This is the exact child we created, never a PID/name search.
		if err := p.cmd.Process.Kill(); err != nil {
			return err
		}
		<-p.done
		return nil
	}
}
func drain(ctx context.Context, c DeviceConfig) error {
	if err := WritePrivate(c.state("drain"), []byte("waiting for existing requests")); err != nil {
		return err
	}
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		var status struct {
			ActiveRequests int `json:"activeRequests"`
		}
		if err := localGet(ctx, c, "/api/fleet/status", &status); err != nil {
			return err
		}
		if status.ActiveRequests == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("active streams defer deployment until next poll")
		case <-tick.C:
		}
	}
}
