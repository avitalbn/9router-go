// 9router-fleet owns a local gateway process and applies signed fleet state.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"9router/proxy/internal/fleet"
)

func main() {
	if err := run(); err != nil {
		if errors.Is(err, fleet.ErrAgentUpdated) {
			os.Exit(75)
		}
		fmt.Fprintln(os.Stderr, "fleet:", err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: 9router-fleet run|export|inspect|snapshot|backup|keygen|sign-release|status [options]")
	}
	command := os.Args[1]
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	config := f.String("config", "", "absolute device configuration file")
	db := f.String("db", "", "SQLite database path")
	out := f.String("out", "", "private output file")
	epoch := f.String("epoch", "", "authority epoch")
	device := f.String("device", "", "enrolled export recipient")
	assets := f.String("assets", "", "directory of cross-platform binaries")
	commit := f.String("commit", "", "40-character source commit")
	key := f.String("key-file", "", "private signing seed file (or FLEET_SIGNING_KEY environment)")
	sequence := f.Uint64("sequence", 0, "monotonic build sequence (GitHub run id)")
	snapshot := f.String("snapshot", "", "portable snapshot file for local rehearsal")
	if err := f.Parse(os.Args[2:]); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	switch command {
	case "launch":
		return fleet.Dispatch(ctx, *config, "launch", "")
	case "export-current":
		return fleet.Dispatch(ctx, *config, "export", *device)
	case "keygen":
		if *out == "" {
			return fmt.Errorf("--out is required")
		}
		public, err := fleet.GenerateKey(*out)
		if err != nil {
			return err
		}
		fmt.Println(public)
		return nil
	case "inspect", "snapshot":
		s, err := fleet.Export(ctx, *db, *epoch)
		if err != nil {
			return err
		}
		if command == "snapshot" {
			s.Secrets = nil
			if *out == "" {
				return fmt.Errorf("--out is required; snapshots may contain manual keys")
			}
			return fleet.WriteJSON(*out, s)
		}
		counts := map[string]int{}
		for t, rows := range s.Tables {
			counts[t] = len(rows)
		}
		for scope, rows := range s.KV {
			counts[scope] = len(rows)
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"schema": s.Schema, "hash": s.Hash, "counts": counts})
	case "backup":
		if *out == "" {
			return fmt.Errorf("--out is required")
		}
		return fleet.Backup(ctx, *db, *out)
	case "rehearse":
		var s fleet.Snapshot
		if *out == "" || *snapshot == "" {
			return fmt.Errorf("--snapshot and a new --out path are required")
		}
		if err := fleet.ReadJSON(*snapshot, &s); err != nil {
			return err
		}
		if err := fleet.Rehearse(ctx, *db, *out, s); err != nil {
			return err
		}
		fmt.Println("rehearsal passed: policy converged; local credentials, authorization and runtime preserved")
		return nil
	case "sign-release", "manifest":
		return releaseFile(*assets, *commit, *sequence, *key, *out, command == "sign-release")
	case "run", "export", "status":
		c, err := fleet.LoadConfig(*config)
		if err != nil {
			return err
		}
		if command == "run" {
			return fleet.Run(ctx, c)
		}
		if command == "export" {
			b, err := fleet.Publish(ctx, c, *device)
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(b)
		}
		var status fleet.SyncStatus
		if err = fleet.ReadJSON(filepath.Join(c.Root, "status.json"), &status); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(status)
	default:
		return fmt.Errorf("unknown fleet operation %q", command)
	}
}
func releaseFile(dir, commit string, sequence uint64, keyFile, out string, sign bool) error {
	if dir == "" || out == "" {
		return fmt.Errorf("--assets and --out are required")
	}
	seed := os.Getenv("FLEET_SIGNING_KEY")
	if keyFile != "" {
		b, err := os.ReadFile(keyFile)
		if err != nil {
			return err
		}
		seed = strings.TrimSpace(string(b))
	}
	r := fleet.Release{Repository: fleet.Repository, Commit: commit, Sequence: sequence, Schema: fleet.SchemaVersion, Assets: map[string]fleet.Asset{}}
	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, file := range files {
		name := file.Name()
		if file.IsDir() || (!strings.HasPrefix(name, "9router-go-") && !strings.HasPrefix(name, "9router-fleet-")) {
			continue
		}
		h, err := fleet.FileDigest(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		r.Assets[name] = fleet.Asset{URL: "https://github.com/" + fleet.Repository + "/releases/download/fleet-" + commit + "/" + name, SHA256: h}
	}
	if len(r.Assets) == 0 {
		return fmt.Errorf("no fleet binaries in %s", strconv.Quote(dir))
	}
	if err = r.Validate(); err != nil {
		return err
	}
	if !sign {
		return fleet.WriteJSON(out, r)
	}
	signed, err := fleet.Sign(r, strings.TrimSpace(seed))
	if err != nil {
		return err
	}
	return fleet.WriteJSON(out, signed)
}
