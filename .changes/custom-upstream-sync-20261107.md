### Changed

- Synced `luqman-v1/9router-go` through `8b93e9de` (`1.9.16-exp.7`). Native
  Responses replay remains intact. Copilot keeps runtime endpoint discovery
  alongside upstream GPT routing, while request diagnostics retain custom
  request IDs and attempt trails with loop warnings, and combo candidates
  retain response timeouts with upstream seat-attempt budgets.
- Validation passed all 399 integration tests and the five cgo-disabled
  release builds; the frontend passed 796 unit tests and its Svelte ratchet.
  Conflicting fixtures were corrected so private-gateway checks stay offline
  and Claude endpoint isolation is tested against the native Messages route.
  Existing macOS platform-test failures remain. The inherited quality scan
  scores 62 on both the baseline and merged trees.
