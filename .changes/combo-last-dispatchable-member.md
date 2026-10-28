### fix(combos): spare the last dispatchable member, name missing connections

- **Response timeout no longer cuts off the last member actually tried (#776).**
  "Last" was the last index, so when the final entry was going to be skipped
  (model-locked, unresolvable, or with no usable connection) the member before
  it still got a timer and the turn answered `504 model response timeout`
  where the same combo without the setting answered. Both combo loops (chat and
  Messages/Responses) now ask whether any later entry can still be dispatched,
  using a read-only check that mirrors the loop's own skips
  (`comboEntryTriable`; `getBestConnection` is not reused because it advances
  connection rotation). The same decision drives the zero-content probe, which
  had the same blind spot. The no-connection case was not listed in #776 and
  matters most for combos that mix connected and unconnected providers.
- **A combo none of whose members has a connection names the reason.** It
  answered `502 all combo models failed: no valid entries`; it now answers
  `502 all combo models failed: no active connections for provider: xai; ...`,
  matching what a single unconnected model returns. A member that reached its
  upstream still wins: its error (401, 429, ...) is what the client sees.
- Tests: `TestComboTimeoutSparesLastDispatchableMember` (locked, unresolvable,
  unconnected final entry plus a dispatchable control, chat/Messages ×
  stream/non-stream; fails on the index check), `TestComboNamesMissingConnections`,
  `TestComboUpstreamErrorOutranksMissingConnection`. The stalled upstreams in
  `TestComboModelResponseTimeoutEndpoints` and `TestComboTimeoutGracePeriod` now
  read the request body and assert the timed-out request is aborted — before,
  they never consumed the body, so net/http could not report the hang-up and a
  gateway that left abandoned requests running would have passed. New
  `combo_timeout_smart_test.go` covers the timeout under Smart Router (System
  One/Jev) routing.
