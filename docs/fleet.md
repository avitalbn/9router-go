# Fork-managed local gateways

Fleet mode runs independent gateways on Windows amd64, macOS amd64/arm64 and
Linux amd64/arm64. Code comes only from this fork. One configuration authority
publishes shared routing state; each participant keeps its own database,
credentials, machine identity and usage history.

## Components and trust

- `9router-go`: gateway, read-only shared settings on followers, probation and
  drain gates, local credential-readiness checks.
- `9router-fleet`: signed snapshot publisher, SSH client, deployment journal,
  private backup/restore, and foreground gateway supervisor.
- `.github/workflows/fleet.yml`: tests the fork, builds all Makefile targets,
  and publishes commit-addressed binaries and a manifest. GitHub OIDC build
  attestations establish provenance without a private signing key in Actions.
- Authority: verifies the manifest using `gh attestation verify`, pinned to this
  repository, the fleet workflow, `refs/heads/main`, the source commit, and
  GitHub-hosted runners. After a successful local canary it signs the approved
  manifest with a key that stays on the authority.
- Followers: accept only snapshots and releases signed by their enrolled
  authority keys. SSH uses a dedicated identity and pinned host keys.

The authority requires GitHub CLI with attestation verification support and
access to this public repository. Followers need OpenSSH and the two fleet
binaries; no Go, Python, Node, Bun or Git checkout is required.

An approved build can lag main while its tests/build/canary are failing.
The last working gateway continues to serve during network outages. Fleet
mode never installs original-upstream releases or merges code on a device.

## State ownership

Shared: combos (including ordered models and match rules), custom nodes and
capacity limits, connection definitions/priorities/model assignments, proxy
pools and assignments, custom models/capabilities, aliases, disabled models,
pricing, routing/thinking/compression policy and the models.dev capability
catalog. Stable identifiers are retained. Unknown fields fail export with an
explicit classification error instead of silently disappearing.

Local: provider API keys, OAuth/session tokens and account binding metadata;
router client API keys and access rules; dashboard password; JWT/CLI secrets;
machine identity and vault master keys; usage, leases, health, cooldowns and
audit records; ports, paths, Headroom endpoint and OS service state.

Normal enrollment does **not** transfer provider credentials. Existing manual
keys and OAuth sessions are retained. New devices must authenticate their own
accounts. Missing credentials disable only the effective local connection;
the authority's desired enabled state stays intact.

Some provider login flows derive a new connection ID from the new token. If
login creates an extra local row, bind it onto the received definition:
`9router-fleet bind-credentials --config <device.json> --source <new-login-id>
--target <shared-connection-id>`. Use the IDs from the local authenticated
`/api/connections` list. The helper queues a backed-up maintenance operation,
requires the same provider/auth type, transfers only device-local credentials
and account metadata, and removes only the extra login row. Shared names,
IDs, priorities and model choices remain unchanged. It refuses to consume a
fleet-managed source row. Allow the first configuration sync to finish before
authenticating and binding accounts.

An authority can explicitly authorize manual API-key bootstrap for selected
recipients using `secretDevices`. This is opt-in, never exports OAuth tokens,
and never overwrites an existing plaintext or sealed key. Authorize credential
distribution separately before enabling this option.

Proxy URL user/password fields stay local. Configure credentials on each new
device before enabling an authenticated proxy. Strict-proxy configuration is
preserved; missing proxy prerequisites are not an instruction to bypass it.
Credential-bearing arbitrary headers or URL query parameters are refused by
the portable-policy validator instead of being silently distributed.

Follower catalog downloads and automatic model-health policy writers are
disabled; the authority publishes these decisions. Transient availability,
local sessions and live upstream catalogs may still differ by device.

## Enrollment

1. Take local SQLite backups on each device. Do not transfer a full database
   to another participant. Save the old executable and supervisor definition.
2. Obtain the gateway and helper from a verified fork release or over an
   already authenticated/pinned connection to the authority.
3. Create an owner-only fleet directory. On Unix use mode 0700 and umask 077.
   On Windows remove inherited ACLs and grant the owning user and SYSTEM full
   access. Keep all databases, keys, snapshots and logs private.
4. Keep writable data outside the release/source directory. Explicitly set
   the data path in the device configuration. Existing data can stay in place.
5. On the authority run `9router-fleet keygen --out <private-file>` twice, for
   configuration and code approval. The command prints only the public key.
   Private seeds never leave the authority. Back them up privately together
   with the authority epoch and publisher revision record.
6. On each follower generate a dedicated SSH key. Add only its public key to
   the authority's authorized_keys with `restrict,command="..."`. The forced
   command is the authority bootstrap helper's `export-current --config
   <device.json> --device <enrolled-id>`. The key cannot provide a shell or
   forward ports. Copy the authority's already verified SSH host key into the
   follower's dedicated known-hosts file; do not trust ssh-keyscan alone.
7. Configure the JSON examples below with real absolute paths and the public
   keys. Keep the same nonempty epoch on all participants. Stop the previous
   gateway/tray watchdog before starting fleet ownership.
8. Run `9router-fleet launch --config <device.json>` under one OS supervisor.
   The gateway must run in the foreground as the helper's child. Install the
   templates under `deploy/fleet/`; replace all example paths first.

The first follower run is explicit enrollment: it backs up the existing local
database and replaces its managed policy with the authority's snapshot while
preserving local credentials. Later local managed edits are drift and block
sync until reviewed. Do not independently edit a follower's shared settings.

### Authority configuration

```json
{
  "role": "authority",
  "root": "/home/avi/.local/share/9router-go-fleet",
  "dataDir": "/home/avi/.local/share/9router-go-data",
  "host": "127.0.0.1",
  "port": 20130,
  "authorityUrl": "https://YOUR-AUTHORITY:20130",
  "epoch": "YOUR-STABLE-RANDOM-EPOCH",
  "configPublicKey": "BASE64-PUBLIC-KEY",
  "configPrivateKey": "/home/avi/.local/share/9router-go-fleet/config-signing.key",
  "codePublicKey": "BASE64-PUBLIC-KEY",
  "codePrivateKey": "/home/avi/.local/share/9router-go-fleet/code-signing.key",
  "githubCli": "/home/avi/.linuxbrew/bin/gh",
  "candidateUrl": "https://github.com/avitalbn/9router-go/releases/download/fleet-candidate/manifest.json",
  "devices": ["windows-device"],
  "initialBinary": "/absolute/path/to/verified/9router-go"
}
```

### Windows follower configuration

```json
{
  "role": "follower",
  "root": "C:/Users/YOUR_USER/AppData/Local/9router-go-fleet",
  "dataDir": "C:/Users/YOUR_USER/9router-go-data",
  "host": "127.0.0.1",
  "port": 20130,
  "authorityUrl": "https://YOUR-AUTHORITY:20130",
  "epoch": "SAME-AUTHORITY-EPOCH",
  "configPublicKey": "AUTHORITY-CONFIG-PUBLIC-KEY",
  "codePublicKey": "AUTHORITY-CODE-PUBLIC-KEY",
  "sshHost": "avi@YOUR-AUTHORITY-HOSTNAME",
  "sshIdentity": "C:/Users/YOUR_USER/AppData/Local/9router-go-fleet/ssh_ed25519",
  "sshKnownHosts": "C:/Users/YOUR_USER/AppData/Local/9router-go-fleet/known_hosts",
  "initialBinary": "C:/absolute/path/to/verified/9router-go.exe"
}
```

Native macOS and other Linux followers use the same schema with Unix paths.
LaunchAgents and per-user Windows tasks start at login. Linux user services
need a running user manager; configure lingering explicitly for unattended boot.
No task should run concurrently with a second tray/daemon launcher.

The gateway inherits existing local .env settings from its data-directory
working directory. The helper supplies DATA_DIR, DB_PATH, HOST, PORT and fleet
variables explicitly. Built-in binary auto-update is disabled regardless of
the stored autoUpdate value.

## Deployment and recovery

The helper checks configuration about once per minute, with jitter, and checks
the authority's candidate code channel every five minutes. No content change
means no restart. Artifacts are authenticated, architecture-checked, checked
against embedded Git revision and SHA256, and staged before maintenance.

For a real change the helper blocks new inference and waits up to 30 seconds
for existing requests. Busy streams defer the update. It stops its owned child,
takes a consistent SQLite `VACUUM INTO` backup (including WAL contents), writes
a recovery journal, applies only managed fields in a transaction and starts the
candidate in probation mode. Health, local dashboard authentication and a
local-key-authenticated model listing must pass.

Probation blocks ordinary traffic and background credential refresh. Live model
discovery is also suppressed during the probe so it cannot rotate an OAuth
token before rollback. An active local plaintext client API key is currently
required for the models probe; do not remove the last one without supplying a
replacement probe mechanism.

Only after successful probation are normal requests and background writers
released. A failed candidate restores the previous database, catalog and
executable pointer. A crash with a pending journal follows the same recovery
path on restart. A failed code/config pair is held for review rather than
restarted endlessly; inspect the private logs and clear `failedRevision` in
status.json to retry after correcting the cause.

After normal traffic has resumed, prefer a forward fix or schema-compatible
binary rollback preserving current runtime state. An old disaster-recovery
database can contain stale OAuth refresh tokens and may need reauthentication.

## Inspection and rehearsal

- `9router-fleet inspect --db <path> --epoch <epoch>` prints counts and the
  managed hash, never credential values.
- `9router-fleet snapshot --db <path> --epoch <epoch> --out <private.json>`
  writes a portable projection excluding provider keys and OAuth sessions.
- `9router-fleet rehearse --db <local-db> --snapshot <portable.json>
  --out <new-private-directory/db/data.sqlite>` backs up the local database to
  a new path, applies there, and verifies that local credentials, client
  authorization and runtime records survive. It never sends a database.
- `9router-fleet status --config <device.json>` prints safe sync status.
- Authenticated `GET /api/fleet/status` reports running commit, role, current
  revision/hash, latest observed fork HEAD, last success/error, active requests
  and maintenance gates. A failed/pending fork build is reported explicitly.
  The dashboard displays the same fleet status and authority link.

Keep the entire private fleet root for recovery: signing keys, epoch,
publisher.json, active.json, status.json, pending.json, approved release,
backups and logs. Never commit those files to the source repository.

If a follower has intentional drift, save a private snapshot, make the desired
edit on the authority, then clear the follower status's stored hash to perform
explicit re-enrollment. Existing local credentials remain untouched. Do not
clear its revision/epoch checks to accept an old authority snapshot.

## Testing

The fleet tests cover transactional dry run, ordered model/policy convergence,
deletions, idempotence, preservation of local credentials and authorization,
sealed credentials, WAL-aware backup/restore, signature tampering, unknown
fields, wrong-fork artifacts, follower mutation restrictions and probation.
The fork workflow also runs the complete Go suite and Svelte type-check gate.
Native macOS runtime qualification is separate from successful cross-compilation.
