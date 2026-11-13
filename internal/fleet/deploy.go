package fleet

import (
	"context"
	"fmt"
	"os"
)

func (g *agent) deploy(ctx context.Context, next Active, s Snapshot) error {
	return g.deployWith(ctx, next, s, func() error {
		if g.config.Role == Follower {
			return Apply(ctx, g.config.DBPath(), s, false)
		}
		return nil
	})
}
func (g *agent) deployWith(ctx context.Context, next Active, s Snapshot, change func() error) error {
	c := g.config
	previousStatus := g.status
	defer func() { _ = os.Remove(c.state("drain")) }()
	if err := drain(ctx, c); err != nil {
		return err
	}
	if err := stopChild(c, g.process); err != nil {
		return err
	}
	pending, err := prepareBackup(ctx, c, g.active, next)
	if err != nil {
		g.process, _ = startChild(c, g.active)
		_ = os.Remove(c.state("probation"))
		return err
	}
	rollback := func(cause error) error {
		if err := stopChild(c, g.process); err != nil {
			return fmt.Errorf("stop candidate for rollback: %w", err)
		}
		if err := restore(c, pending); err != nil {
			return fmt.Errorf("rollback failed: %w", err)
		}
		g.status = previousStatus
		g.process, err = startChild(c, g.active)
		if err != nil {
			return err
		}
		if err = waitHealthy(ctx, c, g.active); err != nil {
			return fmt.Errorf("last-good gateway recovery: %w", err)
		}
		_ = os.Remove(c.state("probation"))
		return fmt.Errorf("%w: %w", ErrRolledBack, cause)
	}
	if err = change(); err != nil {
		return rollback(err)
	}
	if err = WriteJSON(c.state("active.json"), next); err != nil {
		return rollback(err)
	}
	g.process, err = startChild(c, next)
	if err != nil {
		return rollback(err)
	}
	if err = waitHealthy(ctx, c, next); err != nil {
		return rollback(err)
	}
	if c.Role == Follower {
		local, checkErr := Export(ctx, c.DBPath(), c.Epoch)
		if checkErr != nil {
			return rollback(checkErr)
		}
		if local.Hash != s.Hash {
			return rollback(fmt.Errorf("post-apply managed hash mismatch"))
		}
	}
	// Persist the acknowledged baseline while rollback is still possible. A
	// crash between acknowledgement and journal removal restores both together.
	if s.Tables != nil {
		g.status.Commit = next.Commit
		if err = g.acknowledge(s); err != nil {
			return rollback(err)
		}
	}
	// Clear the journal before allowing credential refresh/ordinary traffic.
	// A crash after this point resumes the accepted release, never stale tokens.
	if err = os.Remove(c.state("pending.json")); err != nil {
		return rollback(err)
	}
	g.active = next
	return os.Remove(c.state("probation"))
}
