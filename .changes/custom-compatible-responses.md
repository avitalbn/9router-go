### Fixed

- Custom OpenAI-compatible Responses nodes translate Chat Completions requests
  and replies. Native Responses requests retain their replay fields; standard
  tools remain at the top level with reasoning enabled. Named Responses function
  and custom tool choices convert to the Chat Completions function shape for
  chat-only nodes. Verified with targeted regression tests in the executor and
  translator packages, plus chat-handler coverage for native replay fields.
