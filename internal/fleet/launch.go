package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Dispatch reads only the stable root/active-file contract. The immutable
// bootstrap can therefore launch a newer helper without parsing its schema.
func Dispatch(ctx context.Context, configPath, operation, device string) error {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var base struct {
		Root string `json:"root"`
	}
	if err = json.Unmarshal(raw, &base); err != nil {
		return err
	}
	if !filepath.IsAbs(base.Root) {
		return fmt.Errorf("fleet root must be absolute")
	}
	for {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		var active Active
		if err = ReadJSON(filepath.Join(base.Root, "active.json"), &active); err == nil && active.Agent != "" {
			exe = active.Agent
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		childOperation := operation
		if operation == "launch" {
			childOperation = "run"
		}
		args := []string{childOperation, "--config", configPath}
		if device != "" {
			args = append(args, "--device", device)
		}
		// Graceful child shutdown precedes forced cancellation below.
		cmd := exec.CommandContext(context.Background(), exe, args...)
		quietCommand(cmd)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err = cmd.Start(); err != nil {
			return err
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err = <-done:
			if operation != "launch" {
				return err
			}
		case <-ctx.Done():
			// The owned helper notices its gateway exit and returns. If it cannot,
			// terminate only the helper child created above; the OS service owns its
			// process group as the final cleanup boundary.
			if c, err := LoadConfig(configPath); err == nil {
				_ = shutdownGateway(context.Background(), c)
			}
			select {
			case childErr := <-done:
				if childErr != nil {
					log.Printf("fleet helper stopped: %v", childErr)
				}
			case <-time.After(20 * time.Second):
				_ = cmd.Process.Kill()
				if childErr := <-done; childErr != nil {
					log.Printf("fleet helper terminated: %v", childErr)
				}
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(5 * time.Second):
		}
	}
}
