### fix(github): use the authenticated Copilot catalog and the `/responses` endpoint

The static Copilot seed refresh (upstream decolua/9router dbdbb648) is already
in the released changelog. On top of it:

The provider dashboard now fetches the authenticated Copilot account's `/models`
catalog, including business-account endpoint discovery and declared reasoning
levels/context limits. Check Latest Models and model import use that catalog;
the static seed is not an entitlement guarantee.

Responses-only GPT models now use Copilot's `/responses` endpoint. Chat clients
receive converted SSE or aggregated Chat Completions JSON, while native Responses
clients preserve replay fields and their streaming preference. Converted Chat
replies retain the resolved model identity. The executor
learns endpoint-specific chat refusals, never escalates Claude/Gemini to the
OpenAI-only endpoint, and preserves upstream model/auth/context errors.

Verified with focused GitHub executor/catalog and Responses-handler regression
tests, live account-catalog discovery, and streaming/non-streaming gateway
requests. `gh/gpt-6.1-sol` returned HTTP 200 through both deployed instances.
Account-specific `model_not_supported` refusals remain visible rather than
being treated as missing model definitions.
