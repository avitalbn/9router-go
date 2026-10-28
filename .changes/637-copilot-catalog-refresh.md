### fix(github): refresh Copilot fallback model catalog

The static Copilot fallback list still carried retired models
(gpt-5.2/-codex, opus-4.5/4.7, sonnet-4.5, gemini-2.5-pro,
grok-code-fast-1, oswe-vscode-prime, goldeneye-free-auto) and missed
everything Copilot has shipped since. The fallback default is now
gpt-5-mini, and the list matches GitHub's supported-models documentation
and the Copilot /models catalog as of 2026-10-19: current GPT-5.5/5.6
and GPT-6 lines, the Claude Fable/Opus/Sonnet sets, Gemini 3.7/3.8/4
Argon, Kimi K3 and MAI-Code-1.1-Flash. Embeddings stay.

Live account discovery remains authoritative; the seed refresh follows upstream
decolua/9router dbdbb648.

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
