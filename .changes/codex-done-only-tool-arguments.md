### fix(codex): emit finalized tool arguments once across Responses snapshots

- Resolve Responses `item_id` values to the canonical `call_id` before accessing tool names, indices, or argument buffers. Argument events that omit `call_id` no longer share an empty-key buffer across parallel calls.
- Handle `response.function_call_arguments.done` and `response.output_item.done` as full snapshots of the same per-call argument stream. Forward only the missing suffix, preserving repeated incremental fragments and completing partial arguments without emitting a second JSON object.
- Remove the separate `ArgsEmitted` flag: the per-call argument buffer now governs both finalization paths, including streams that deliver no argument deltas. This prevents valid Codex tool calls from being rejected by clients such as Hermes as malformed or length-truncated replies.
- Add eight consumer-facing streaming regression scenarios covering done-only parallel calls, interleaved and repeated deltas, partial arguments, repeated final snapshots, and call-id-only or output-item-only delivery.
- Verified with executor and chat-handler package tests, a release binary build, live streaming tool-result roundtrips through direct Codex and the `codex`/`smart` combos, non-streaming Chat Completions, native Responses passthrough, and Hermes's actual tool-argument assembler.
