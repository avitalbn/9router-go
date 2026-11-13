package fleet

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// A newer configuration can introduce fields the running helper cannot apply.
// Upgrade the verified helper independently, keeping the old gateway/data
// selected, then let the new helper validate the code/config pair.
func (g *agent) upgradeHelperForSchema(ctx context.Context, bundle Bundle, cause error) error {
	c := g.config
	signed := bundle.Release
	if c.Role == Authority && c.CandidateURL != "" {
		candidate, err := FetchAttested(ctx, c, c.CandidateURL)
		if err != nil {
			return fmt.Errorf("%w; helper channel: %w", cause, err)
		}
		signed = &candidate
	}
	if signed == nil {
		return cause
	}
	next, err := StageRelease(ctx, c, *signed, g.active)
	if err != nil {
		return fmt.Errorf("%w; helper staging: %w", cause, err)
	}
	current, err := os.Executable()
	if err != nil {
		return err
	}
	if next.Agent == "" || filepath.Clean(current) == filepath.Clean(next.Agent) {
		return cause
	}
	helperOnly := g.active
	helperOnly.Agent = next.Agent
	if err = WriteJSON(c.state("active.json"), helperOnly); err != nil {
		return err
	}
	g.active = helperOnly
	return ErrAgentUpdated
}
