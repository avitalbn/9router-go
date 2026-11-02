# Tracing combo requests

Open **Usage → Details**, filter by the requested combo (for example
`agent-general`), and open a request row. **Model & Account Chain** shows all
recorded attempts through that row, including the row's own model. A successful
row contains the preceding failures and the winning model. Unavailable members
appear as **Skipped**, since no provider HTTP request occurred.

Each row also carries a **Request ID**, shared by every fallback in the inbound
request. Harnesses can send `X-Request-ID` and read it back from the response.
Paste that ID into the Details filter to match rows on the current page. The
filter searches the loaded page; it is not a server-wide historical search.
Historical rows written before this change have no correlation field and may
omit their current attempt; their missing data cannot be reconstructed.

Interpret the chain:

| Result | Meaning |
| --- | --- |
| 200 / success | This provider served a completion |
| 402 / quota exceeded | Provider quota or account/key spend limit |
| 429 / rate limited | Provider throttling; inspect its error for reset guidance |
| 504 / timeout | Gateway/provider response deadline, separate from client cancellation |
| 499 / client canceled | Request was canceled by its caller |
| Skipped / unavailable | No dispatch: connection unavailable, model cooldown, or unresolved entry |

The response timeout is a candidate budget, not an overall combo deadline.
Existing behavior preserves the global deadline on the final dispatchable
candidate. Time spent on earlier candidates accumulates. Capability ordering,
configured rotation, smart routing and temporary cooldowns may change the order
from the stored list. Streams that have already committed usable output cannot
switch to another provider mid-response.

Trails retain at most 64 entries. A limit notice appears when that bound is
reached; the current row's attempt is retained. Diagnostic rows follow the
existing observability and retention settings, so `off` disables them and
`errors_only` omits successful rows.

## Live agent-general diagnosis, 2026-10-29

The configured top-level chain during investigation was:

`gh/gpt-6.1-sol → cx/gpt-6.1-sol → nous-paid/openai/gpt-6.1-sol → general-free`

The response timeout was 6,000 ms and timeout grace was 3,000 ms.

A bounded live Chat Completions probe with correlation ID
`combo-diagnostic-20261029` returned HTTP 200 and `OK` in 5,733 ms. Its stored
rows identify Copilot as the first dispatched provider, returning HTTP 402,
“You have exceeded your monthly quota”, in approximately 1 second. Codex then
served the successful completion in approximately 4.6 seconds. The old success
row displayed only the Copilot failure and omitted Codex from its attempt trail.
This was a logging defect, not evidence that Codex was skipped.

Earlier stored requests showed a separate API-key spend-limit 402 followed by
successful Beam fallback. Those rows predate the latest combo configuration and
must not be read as evidence of the current chain. No timeout was observed in
the bounded live probe. The 6-second candidate budget can still matter for
slower requests; the diagnostics distinguish this from quota failures.

The investigation did not change combo membership, ordering, credentials or
provider quota settings. A harness-specific tool or streaming failure needs its
own request trace; a short non-streaming probe does not establish those paths.

## Validation notes

Affected Go packages (`internal/handlers/chat`, `internal/db`,
`internal/handlers`, `web`) and the targeted production-router integration cases
passed. The frontend suite passed 669 tests; the Svelte ratchet retained its
77-error baseline with zero unresolved identifiers. The production build is
5.0 MiB on disk. The main JavaScript Brotli file grew by approximately 115 bytes.

BrowserOS verified the attempt-chain modal against an isolated gateway and
synthetic providers. An origin-cleared, HTTP-cache-disabled preview navigation
transferred 739,339 bytes over 21 requests with a 2,279 ms load event. This was
one local browser sample, not a fresh browser-context timing study.

A preliminary 100-request synthetic run with GOMAXPROCS=1 measured 42.34 MiB
RSS / 15.3 ms CPU per request for the installed exp.10 binary and 44.12 MiB /
8.1 ms for the workspace exp.13 build. These are different versions and one
short sample, so the differences cannot be attributed to this patch or used as
a steady-state regression verdict. Trails are bounded; no persistent trace
cache or background polling was added.

The live gateway and its combo configuration were left unchanged. The updated
binary was exercised on a separate loopback port with no real provider keys.
