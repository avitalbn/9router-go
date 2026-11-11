package executor

import (
	"fmt"
	"net/http"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

// forwardOpenAIResponses uses the configured Responses lane without changing
// the client's wire format. Chat clients consume translated upstream SSE;
// native Responses clients retain their body and streaming preference.
func forwardOpenAIResponses(w http.ResponseWriter, req *Request) error {
	body := req.Body
	native := clientFormatFor(reqCtx(req)) == providers.FormatOpenAIResponses
	if !native {
		var err error
		body, _, err = buildResponsesBody(body)
		if err != nil {
			return fmt.Errorf("ForwardOpenAI responses body: %w", err)
		}
	}
	resp, err := proxy.ForwardOpenAI(reqCtx(req), req.Client, req.Config, req.APIKey, body, req.IsStream || !native)
	if err != nil {
		return fmt.Errorf("ForwardOpenAI responses upstream: %w", err)
	}
	defer resp.Body.Close()
	forwardUpstreamResponseHeaders(w, resp.Header)
	if err := upstreamStatusError(resp); err != nil {
		return err
	}
	if req.IsStream {
		reader := proxy.NewStallReaderWithContext(reqCtx(req), resp.Body, req.Config.ResolveStallTimeout(proxy.ResolveDefaultStallTimeout()), "openai-responses")
		defer reader.Close()
		return handleCodexStream(w, req, reader)
	}
	return handleCodexStream(w, req, resp.Body)
}
