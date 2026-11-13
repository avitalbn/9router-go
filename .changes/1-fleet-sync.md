### Fork-managed gateway fleet

- Add a portable fleet supervisor with signed SSH configuration snapshots,
  explicit shared/local ownership, private transactional backup and recovery,
  native supervisor templates and a cross-platform enrollment runbook.
- Publish fork-only commit-addressed builds with keyless GitHub provenance;
  verify on the authority and approve there before followers install.
- Preserve existing device credentials, authorization and runtime records;
  block follower shared-setting writes and independent catalog policy writers.
- Gate probation traffic, OAuth refresh and live catalog discovery so a failed
  candidate can restore the last good database without rewinding rotated tokens.
- Include local rollback, tampering, unknown-schema, ordering and credential
  preservation tests. Allow equal timestamps at the already capped ban window
  on Windows without weakening escalation assertions for growing windows.
- Keep local reauthentication compatible with shared IDs through an explicit
  same-provider credential-binding operation. Add a fresh-context dashboard
  check and update the request-detail fixture to include its trace request ID.
