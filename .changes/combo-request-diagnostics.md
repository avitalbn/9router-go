### Combo request diagnostics

- Show provider/model, account, start time, duration, response timeout, HTTP status and typed failure reason for each combo hop, including skipped connections and single-attempt requests.
- Include the current failing or winning attempt in its own persisted row. Previously rows contained only earlier failures; successful combo attempts could also be recorded as HTTP 0.
- Serialize request start times in UTC before adding the `Z` suffix, and distinguish the request correlation ID from the stored detail ID.
- Preserve request correlation IDs in full and summary rows and allow filtering the current dashboard page by combo or request ID. Trails remain bounded to 64 entries, with a visible limit notice.
- Distinguish gateway response timeouts (504) from client disconnects (499), quota/spend limits (402), and rate limits (429). Record a failure when every combo member is unavailable before dispatch.
- Behavioral reference: decolua/9router `master/open-sse/services/combo.js` fallback and capability ordering. Diagnostic fields are additive; provider ordering and quota policies remain unchanged.
- Validation: unit classification and persisted timeout cases, production-router integration coverage for quota fallback and unavailable members, Svelte ratchet and production dashboard build.
