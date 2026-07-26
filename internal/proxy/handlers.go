// handlers.go registers the OpenAI-compatible endpoints on the chi router.
//
// Streaming note (Phase 4 v1): response bodies are buffered in full inside
// ModifyResponse before being released to the client. This sacrifices real-time
// streaming UX for reliable usage parsing and request logging. A future phase
// can replace this with a teeing ResponseWriter that parses usage on the fly.
package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"strings"
	"time"

	"autoapi/internal/model"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

const maxBodySize = 10 << 20        // 10 MiB — embeddings and other small endpoints
const maxChatBodySize = 50 << 20    // 50 MiB — chat completions (vision/multimodal with base64 images)
const maxGenericBodySize = 50 << 20 // 50 MiB — generic OpenAI (audio transcription, file uploads, images)

// responsesRequest is deliberately kept as raw JSON. Responses is a native
// pass-through endpoint; the proxy must not translate it to Chat Completions.
type responsesRequest struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
}

type messagesRequest struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
}

func validateResponsesRequest(body []byte) (responsesRequest, error) {
	var req responsesRequest
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return req, err
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return req, err
	}
	if req.Model == "" {
		return req, errors.New("model is required")
	}
	for field := range fields {
		switch field {
		case "previous_response_id", "conversation":
			return req, errors.New("stateful Responses fields are not supported: " + field)
		case "background":
			var v bool
			if json.Unmarshal(fields[field], &v) == nil && v {
				return req, errors.New("background Responses execution is not supported")
			}
		case "store":
			var v bool
			if json.Unmarshal(fields[field], &v) == nil && v {
				return req, errors.New("store=true is not supported with failover")
			}
		}
	}
	return req, nil
}

func validateMessagesRequest(body []byte) (messagesRequest, error) {
	var req messagesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return req, err
	}
	if req.Model == "" {
		return req, errors.New("model is required")
	}
	return req, nil
}

func (p *Proxy) handleResponses(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	logEntry := &model.RequestLog{Timestamp: start.UnixMilli()}
	enrichLogFromRequest(r, logEntry)
	defer p.logRequestEntry(logEntry)
	defer p.emitRequest(logEntry, r.URL.Path)
	defer func() { logEntry.LatencyMs = int(time.Since(start).Milliseconds()) }()

	apiKeyID, apiKeyName, allowedRuleIDs, ok, err := p.authenticate(r)
	if err != nil || !ok {
		status := http.StatusUnauthorized
		if err != nil {
			status = http.StatusInternalServerError
		}
		p.writeError(w, status, "invalid_request_error", "Invalid API key")
		logEntry.StatusCode, logEntry.Error = status, "authentication failed"
		return
	}
	logEntry.APIKeyID, logEntry.APIKeyName = apiKeyID, apiKeyName
	body, err := io.ReadAll(io.LimitReader(r.Body, maxChatBodySize))
	if err != nil {
		p.writeError(w, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		logEntry.StatusCode, logEntry.Error = http.StatusBadRequest, err.Error()
		return
	}
	_ = r.Body.Close()
	respReq, reqs, err := parseAndInspectResponses(body)
	if err != nil {
		p.writeError(w, http.StatusUnprocessableEntity, "invalid_request_error", err.Error())
		logEntry.StatusCode, logEntry.Error = http.StatusUnprocessableEntity, err.Error()
		return
	}
	logEntry.Model, logEntry.RouteLabel, logEntry.IsStream = respReq.Model, respReq.Model, respReq.Stream
	logEntry.ReasoningEffort = respReq.ReasoningEffort
	inbound := &InboundRequest{Model: respReq.Model, Header: extractHeaders(r.Header), Task: "responses", TimeHour: time.Now().Hour(), Endpoint: "/v1/responses", Stream: respReq.Stream, Protocol: ProtocolOpenAIResponses, Requirements: reqs, AllowedRuleIDs: allowedRuleIDs}
	p.insertPendingLog(logEntry)
	candidates, err := p.resolveCandidates(inbound)
	if err != nil {
		errType := "service_unavailable"
		switch {
		case errors.Is(err, errNoMatch):
			errType = "no_matching_rule"
		case errors.Is(err, errUnsupportedFeature):
			errType = "unsupported_feature"
			p.appendUnsupportedFeaturePreflightChain(logEntry, inbound, err)
			p.writeError(w, http.StatusUnprocessableEntity, errType, err.Error())
			logEntry.StatusCode, logEntry.Error = http.StatusUnprocessableEntity, err.Error()
			return
		}
		p.writeError(w, http.StatusServiceUnavailable, errType, err.Error())
		logEntry.StatusCode, logEntry.Error = http.StatusServiceUnavailable, err.Error()
		return
	}
	if respReq.Stream && hasUnsupportedStreamConversion(candidates, ProtocolOpenAIResponses) {
		msg := "Streaming protocol conversion is not yet supported"
		p.writeError(w, http.StatusUnprocessableEntity, "unsupported_feature", msg)
		logEntry.StatusCode, logEntry.Error = http.StatusUnprocessableEntity, msg
		return
	}
	// forwardWithFailover preserves the original JSON and uses r.URL.Path,
	// therefore every upstream attempt is exactly POST /v1/responses.
	p.forwardWithFailover(w, r, body, candidates, respReq.Stream, 0, logEntry)
}

func (p *Proxy) handleMessages(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	logEntry := &model.RequestLog{Timestamp: start.UnixMilli()}
	enrichLogFromRequest(r, logEntry)
	defer p.logRequestEntry(logEntry)
	defer p.emitRequest(logEntry, r.URL.Path)
	defer func() { logEntry.LatencyMs = int(time.Since(start).Milliseconds()) }()

	apiKeyID, apiKeyName, allowedRuleIDs, ok, err := p.authenticate(r)
	if err != nil || !ok {
		status := http.StatusUnauthorized
		if err != nil {
			status = http.StatusInternalServerError
		}
		p.writeError(w, status, "invalid_request_error", "Invalid API key")
		logEntry.StatusCode, logEntry.Error = status, "authentication failed"
		return
	}
	logEntry.APIKeyID, logEntry.APIKeyName = apiKeyID, apiKeyName
	body, err := io.ReadAll(io.LimitReader(r.Body, maxChatBodySize))
	if err != nil {
		p.writeError(w, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		logEntry.StatusCode, logEntry.Error = http.StatusBadRequest, err.Error()
		return
	}
	_ = r.Body.Close()
	msgReq, reqs, err := parseAndInspectMessages(body)
	if err != nil {
		p.writeError(w, http.StatusUnprocessableEntity, "invalid_request_error", err.Error())
		logEntry.StatusCode, logEntry.Error = http.StatusUnprocessableEntity, err.Error()
		return
	}
	logEntry.Model, logEntry.RouteLabel, logEntry.IsStream = msgReq.Model, msgReq.Model, msgReq.Stream
	inbound := &InboundRequest{Model: msgReq.Model, Header: extractHeaders(r.Header), Task: "messages", TimeHour: time.Now().Hour(), Endpoint: "/v1/messages", Stream: msgReq.Stream, Protocol: ProtocolAnthropicMessages, Requirements: reqs, AllowedRuleIDs: allowedRuleIDs}
	p.insertPendingLog(logEntry)
	candidates, err := p.resolveCandidates(inbound)
	if err != nil {
		errType := "service_unavailable"
		switch {
		case errors.Is(err, errNoMatch):
			errType = "no_matching_rule"
		case errors.Is(err, errUnsupportedFeature):
			errType = "unsupported_feature"
			p.appendUnsupportedFeaturePreflightChain(logEntry, inbound, err)
			p.writeError(w, http.StatusUnprocessableEntity, errType, err.Error())
			logEntry.StatusCode, logEntry.Error = http.StatusUnprocessableEntity, err.Error()
			return
		}
		p.writeError(w, http.StatusServiceUnavailable, errType, err.Error())
		logEntry.StatusCode, logEntry.Error = http.StatusServiceUnavailable, err.Error()
		return
	}
	if msgReq.Stream && hasUnsupportedStreamConversion(candidates, ProtocolAnthropicMessages) {
		msg := "Streaming protocol conversion is not yet supported"
		p.writeError(w, http.StatusUnprocessableEntity, "unsupported_feature", msg)
		logEntry.StatusCode, logEntry.Error = http.StatusUnprocessableEntity, msg
		return
	}
	p.forwardWithFailover(w, r, body, candidates, msgReq.Stream, 0, logEntry)
}

func (p *Proxy) handleGemini(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	logEntry := &model.RequestLog{Timestamp: start.UnixMilli()}
	enrichLogFromRequest(r, logEntry)
	defer p.logRequestEntry(logEntry)
	defer p.emitRequest(logEntry, r.URL.Path)
	defer func() { logEntry.LatencyMs = int(time.Since(start).Milliseconds()) }()

	apiKeyID, apiKeyName, allowedRuleIDs, ok, err := p.authenticate(r)
	if err != nil || !ok {
		status := http.StatusUnauthorized
		if err != nil {
			status = http.StatusInternalServerError
		}
		p.writeError(w, status, "invalid_request_error", "Invalid API key")
		logEntry.StatusCode, logEntry.Error = status, "authentication failed"
		return
	}
	logEntry.APIKeyID, logEntry.APIKeyName = apiKeyID, apiKeyName

	modelName, action, err := parseGeminiModelAction(chi.URLParam(r, "modelAction"))
	if err != nil {
		p.writeError(w, http.StatusNotFound, "invalid_request_error", err.Error())
		logEntry.StatusCode, logEntry.Error = http.StatusNotFound, err.Error()
		return
	}
	stream := action == "streamGenerateContent"
	if stream {
		q := r.URL.Query()
		q.Set("alt", "sse")
		r.URL.RawQuery = q.Encode()
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxChatBodySize))
	if err != nil {
		p.writeError(w, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		logEntry.StatusCode, logEntry.Error = http.StatusBadRequest, err.Error()
		return
	}
	_ = r.Body.Close()

	gReq, reqs, err := parseAndInspectGemini(body)
	if err != nil {
		p.writeError(w, http.StatusUnprocessableEntity, "invalid_request_error", err.Error())
		logEntry.StatusCode, logEntry.Error = http.StatusUnprocessableEntity, err.Error()
		return
	}
	_ = gReq
	if stream {
		reqs.Features = appendFeature(reqs.Features, model.FeatureStreaming)
	}

	logEntry.Model, logEntry.RouteLabel, logEntry.IsStream = modelName, modelName, stream
	inbound := &InboundRequest{Model: modelName, Header: extractHeaders(r.Header), Task: "gemini", TimeHour: time.Now().Hour(), Endpoint: r.URL.Path, Stream: stream, Protocol: ProtocolGemini, Requirements: reqs, AllowedRuleIDs: allowedRuleIDs}
	p.insertPendingLog(logEntry)
	candidates, err := p.resolveCandidates(inbound)
	if err != nil {
		errType := "service_unavailable"
		switch {
		case errors.Is(err, errNoMatch):
			errType = "no_matching_rule"
		case errors.Is(err, errUnsupportedFeature):
			errType = "unsupported_feature"
			p.appendUnsupportedFeaturePreflightChain(logEntry, inbound, err)
			p.writeError(w, http.StatusUnprocessableEntity, errType, err.Error())
			logEntry.StatusCode, logEntry.Error = http.StatusUnprocessableEntity, err.Error()
			return
		}
		p.writeError(w, http.StatusServiceUnavailable, errType, err.Error())
		logEntry.StatusCode, logEntry.Error = http.StatusServiceUnavailable, err.Error()
		return
	}
	p.forwardWithFailover(w, r, body, candidates, stream, len(body)/16, logEntry)
}

func parseGeminiModelAction(modelAction string) (modelName, action string, err error) {
	colon := strings.LastIndex(modelAction, ":")
	if colon <= 0 || colon == len(modelAction)-1 {
		return "", "", errors.New("Invalid Gemini model action")
	}
	modelName, action = modelAction[:colon], modelAction[colon+1:]
	switch action {
	case "generateContent", "streamGenerateContent":
		return modelName, action, nil
	default:
		return "", "", errors.New("Invalid Gemini model action")
	}
}

func hasConversionCandidate(candidates []candidate) bool {
	for _, c := range candidates {
		if c.convertTo != "" {
			return true
		}
	}
	return false
}

func hasUnsupportedStreamConversion(candidates []candidate, from Protocol) bool {
	for _, c := range candidates {
		if c.convertTo == "" {
			continue
		}
		if !supportsStreamConversion(from, c.convertTo) {
			return true
		}
	}
	return false
}

// clientIPFromAddr extracts the host portion of an HTTP RemoteAddr value
// ("host:port" or, in pathological cases, "host"). It returns "" when
// RemoteAddr is empty or the host cannot be parsed at all — these cases
// are logged as empty in the request log rather than producing a bogus
// value (or panic). Proxies typically inject X-Forwarded-For headers, but
// autoapi runs behind the loopback for local clients, so the raw
// RemoteAddr is the source of truth here.
func clientIPFromAddr(remoteAddr string) string {
	if remoteAddr == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		// Either it was bare host (no port) or something exotic. Fall back
		// to the raw string — it's better than dropping the IP entirely.
		return remoteAddr
	}
	return host
}

// enrichLogFromRequest captures the per-request context fields that the
// proxy stores on every RequestLog: chi's middleware RequestID, the
// User-Agent header, and the client IP (host portion of RemoteAddr). It
// is a no-op for fields that have no source (empty User-Agent, etc.) so
// the JSON omitempty-ish behaviour of the SQL layer is preserved.
func enrichLogFromRequest(r *http.Request, logEntry *model.RequestLog) {
	logEntry.RequestID = middleware.GetReqID(r.Context())
	logEntry.UserAgent = r.UserAgent()
	logEntry.ClientIP = clientIPFromAddr(r.RemoteAddr)
	logEntry.RequestURI = r.URL.Path
}

func (p *Proxy) appendUnsupportedFeaturePreflightChain(logEntry *model.RequestLog, req *InboundRequest, err error) {
	rule, ok := findModelRule(req, p.loadModelRules())
	if !ok || rule == nil {
		return
	}
	logEntry.RouteID = rule.ID
	logEntry.RouteLabel = rule.Name
	attemptOrder := len(logEntry.Chain)
	for _, target := range rule.Targets {
		if !target.Enabled {
			continue
		}
		attemptOrder++
		providerName := ""
		provider, getErr := p.store.GetProvider(target.ProviderID)
		if getErr == nil && provider != nil {
			providerName = provider.Name
		}
		logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
			AttemptOrder: attemptOrder,
			ProviderID:   target.ProviderID,
			ProviderName: providerName,
			ModelName:    modelNameForTarget(target.ModelName, req.Model),
			TargetID:     target.ID,
			Endpoint:     req.Endpoint,
			Status:       string(model.AttemptOutcomePreflightError),
			StatusCode:   0,
			Error:        err.Error(),
		})
	}
}

type chatRequest struct {
	Model    string            `json:"model"`
	Messages []json.RawMessage `json:"messages"`
	Stream   bool              `json:"stream"`
	Task     string            `json:"task"`
}

type embeddingsRequest struct {
	Model string      `json:"model"`
	Input interface{} `json:"input"`
}

type modelItem struct {
	ID     string `json:"id"`
	Object string `json:"object"`
}

func (p *Proxy) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	logEntry := &model.RequestLog{
		Timestamp:     start.UnixMilli(),
		CacheCreation: 0,
		CacheHit:      0,
	}
	enrichLogFromRequest(r, logEntry)
	defer p.logRequestEntry(logEntry)
	defer p.emitRequest(logEntry, r.URL.Path)
	defer func() { logEntry.LatencyMs = int(time.Since(start).Milliseconds()) }()

	apiKeyID, apiKeyName, allowedRuleIDs, ok, err := p.authenticate(r)
	if err != nil || !ok {
		status := http.StatusUnauthorized
		if err != nil {
			status = http.StatusInternalServerError
			slog.Error("proxy: auth failed (internal error)", "apiKeyID", apiKeyID, "path", r.URL.Path, "err", err)
		} else {
			slog.Warn("proxy: auth failed (invalid API key)", "apiKeyID", apiKeyID, "path", r.URL.Path)
		}
		p.writeError(w, status, "invalid_request_error", "Invalid API key")
		logEntry.StatusCode = status
		logEntry.APIKeyID, logEntry.APIKeyName = apiKeyID, apiKeyName
		logEntry.Error = "authentication failed"
		return
	}
	logEntry.APIKeyID, logEntry.APIKeyName = apiKeyID, apiKeyName

	body, err := io.ReadAll(io.LimitReader(r.Body, maxChatBodySize))
	if err != nil {
		slog.Error("proxy: read request body failed", "path", r.URL.Path, "err", err)
		p.writeError(w, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		logEntry.StatusCode = http.StatusBadRequest
		logEntry.Error = err.Error()
		return
	}
	_ = r.Body.Close()
	chatReq, reqs, err := parseAndInspectChat(body)
	if err != nil {
		slog.Warn("proxy: invalid chat request", "path", r.URL.Path, "err", err)
		p.writeError(w, http.StatusUnprocessableEntity, "invalid_request_error", err.Error())
		logEntry.StatusCode = http.StatusUnprocessableEntity
		logEntry.Error = err.Error()
		return
	}
	if chatReq.Task == "" {
		chatReq.Task = "chat"
	}

	logEntry.Model = chatReq.Model
	logEntry.RouteLabel = chatReq.Model
	logEntry.IsStream = chatReq.Stream
	logEntry.ReasoningEffort = chatReq.ReasoningEffort

	inbound := &InboundRequest{
		Model:           chatReq.Model,
		Header:          extractHeaders(r.Header),
		EstimatedTokens: estimateChatInputTokens(len(chatReq.Messages)),
		Task:            chatReq.Task,
		TimeHour:        time.Now().Hour(),
		Endpoint:        r.URL.Path,
		Stream:          chatReq.Stream,
		Protocol:        ProtocolOpenAIChat,
		Requirements:    reqs,
		AllowedRuleIDs:  allowedRuleIDs,
	}
	logEntry.InputTokens = inbound.EstimatedTokens

	// Insert a pending log row immediately so the user can see the
	// in-flight request in the log table before it completes. The same
	// row is updated with final status/tokens/cost/chain by the deferred
	// logRequestEntry when the handler returns.
	p.insertPendingLog(logEntry)

	candidates, err := p.resolveCandidates(inbound)
	if err != nil {
		slog.Warn("proxy: no candidates", "path", r.URL.Path, "model", inbound.Model, "err", err)
		// The "default fallback" feature has been removed: an inbound
		// request whose model is not registered as a model rule now fails
		// loudly with 503 + "no_matching_rule" instead of being silently
		// forwarded to a synthetic default provider.
		errType := "service_unavailable"
		switch {
		case errors.Is(err, errNoMatch):
			errType = "no_matching_rule"
		case errors.Is(err, errUnsupportedFeature):
			errType = "unsupported_feature"
			p.appendUnsupportedFeaturePreflightChain(logEntry, inbound, err)
			p.writeError(w, http.StatusUnprocessableEntity, errType, err.Error())
			logEntry.StatusCode = http.StatusUnprocessableEntity
			logEntry.Error = err.Error()
			return
		}
		p.writeError(w, http.StatusServiceUnavailable, errType, err.Error())
		logEntry.StatusCode = http.StatusServiceUnavailable
		logEntry.Error = err.Error()
		return
	}

	p.forwardWithFailover(w, r, body, candidates, chatReq.Stream, inbound.EstimatedTokens, logEntry)
}

func (p *Proxy) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	logEntry := &model.RequestLog{
		Timestamp:     start.UnixMilli(),
		CacheCreation: 0,
		CacheHit:      0,
	}
	enrichLogFromRequest(r, logEntry)
	defer p.logRequestEntry(logEntry)
	defer p.emitRequest(logEntry, r.URL.Path)
	defer func() { logEntry.LatencyMs = int(time.Since(start).Milliseconds()) }()

	apiKeyID, apiKeyName, allowedRuleIDs, ok, err := p.authenticate(r)
	if err != nil || !ok {
		status := http.StatusUnauthorized
		if err != nil {
			status = http.StatusInternalServerError
			slog.Error("proxy: auth failed (internal error)", "apiKeyID", apiKeyID, "path", r.URL.Path, "err", err)
		} else {
			slog.Warn("proxy: auth failed (invalid API key)", "apiKeyID", apiKeyID, "path", r.URL.Path)
		}
		p.writeError(w, status, "invalid_request_error", "Invalid API key")
		logEntry.StatusCode = status
		logEntry.APIKeyID, logEntry.APIKeyName = apiKeyID, apiKeyName
		logEntry.Error = "authentication failed"
		return
	}
	logEntry.APIKeyID, logEntry.APIKeyName = apiKeyID, apiKeyName

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodySize))
	if err != nil {
		slog.Error("proxy: read request body failed", "path", r.URL.Path, "err", err)
		p.writeError(w, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		logEntry.StatusCode = http.StatusBadRequest
		logEntry.Error = err.Error()
		return
	}
	_ = r.Body.Close()

	var embReq embeddingsRequest
	if err := json.Unmarshal(body, &embReq); err != nil {
		slog.Warn("proxy: invalid JSON body", "path", r.URL.Path, "err", err)
		p.writeError(w, http.StatusBadRequest, "invalid_request_error", "Invalid JSON body")
		logEntry.StatusCode = http.StatusBadRequest
		logEntry.Error = err.Error()
		return
	}

	// Pre-populate log fields so aborts carry the model name.
	logEntry.Model = embReq.Model
	logEntry.RouteLabel = embReq.Model

	inbound := &InboundRequest{
		Model:           embReq.Model,
		Header:          extractHeaders(r.Header),
		EstimatedTokens: estimateEmbeddingInputTokens(embReq.Input) / 4,
		Task:            "embeddings",
		TimeHour:        time.Now().Hour(),
		Endpoint:        r.URL.Path,
		Protocol:        ProtocolOpenAI,
		AllowedRuleIDs:  allowedRuleIDs,
	}
	logEntry.InputTokens = inbound.EstimatedTokens
	p.insertPendingLog(logEntry)

	candidates, err := p.resolveCandidates(inbound)
	if err != nil {
		slog.Warn("proxy: no candidates", "path", r.URL.Path, "model", inbound.Model, "err", err)
		errType := "service_unavailable"
		if errors.Is(err, errNoMatch) {
			errType = "no_matching_rule"
		}
		p.writeError(w, http.StatusServiceUnavailable, errType, err.Error())
		logEntry.StatusCode = http.StatusServiceUnavailable
		logEntry.Error = err.Error()
		return
	}

	p.forwardWithFailover(w, r, body, candidates, false, inbound.EstimatedTokens, logEntry)
}

// genericOpenAIRequest is the minimal body used to route unknown OpenAI
// endpoints. If a model field is present we use it; otherwise the request falls
// back to the default provider.
type genericOpenAIRequest struct {
	Model string `json:"model"`
}

// handleOpenAI proxies any OpenAI-compatible endpoint that does not have a
// dedicated handler (e.g. images, audio, files). It authenticates, reads the
// body, and forwards through the same failover path used by chat/embeddings.
// For streaming endpoints the request should still use the dedicated chat
// handler so that SSE can be delivered in real time.
func (p *Proxy) handleOpenAI(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	logEntry := &model.RequestLog{
		Timestamp:     start.UnixMilli(),
		CacheCreation: 0,
		CacheHit:      0,
	}
	enrichLogFromRequest(r, logEntry)
	defer p.logRequestEntry(logEntry)
	defer p.emitRequest(logEntry, r.URL.Path)
	defer func() { logEntry.LatencyMs = int(time.Since(start).Milliseconds()) }()

	apiKeyID, apiKeyName, allowedRuleIDs, ok, err := p.authenticate(r)
	if err != nil || !ok {
		status := http.StatusUnauthorized
		if err != nil {
			status = http.StatusInternalServerError
			slog.Error("proxy: auth failed (internal error)", "apiKeyID", apiKeyID, "path", r.URL.Path, "err", err)
		} else {
			slog.Warn("proxy: auth failed (invalid API key)", "apiKeyID", apiKeyID, "path", r.URL.Path)
		}
		p.writeError(w, status, "invalid_request_error", "Invalid API key")
		logEntry.StatusCode = status
		logEntry.APIKeyID = ""
		logEntry.Error = "authentication failed"
		return
	}
	logEntry.APIKeyID, logEntry.APIKeyName = apiKeyID, apiKeyName

	body, err := io.ReadAll(io.LimitReader(r.Body, maxGenericBodySize))
	if err != nil {
		slog.Error("proxy: read request body failed", "path", r.URL.Path, "err", err)
		p.writeError(w, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		logEntry.StatusCode = http.StatusBadRequest
		logEntry.Error = err.Error()
		return
	}
	_ = r.Body.Close()

	// For multipart/form-data endpoints (audio transcriptions, file
	// uploads) the model field is in the form, not the JSON body. Try
	// JSON first (images/generations sends JSON), then fall back to
	// parsing the multipart form to extract the model name for routing.
	var modelField string
	if isMultipartRequest(r) {
		modelField = extractMultipartModel(body, r)
	} else {
		var jr struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &jr)
		modelField = jr.Model
	}
	genericReq := genericOpenAIRequest{Model: modelField}

	// Pre-populate log fields so aborts carry the model name.
	logEntry.Model = modelField
	logEntry.RouteLabel = modelField

	var task string
	switch r.URL.Path {
	case "/v1/images/generations":
		task = "images"
	case "/v1/audio/transcriptions":
		task = "audio"
	case "/v1/files":
		task = "files"
	default:
		task = "generic"
	}

	inbound := &InboundRequest{
		Model:           genericReq.Model,
		Header:          extractHeaders(r.Header),
		EstimatedTokens: len(body) / 16,
		Task:            task,
		TimeHour:        time.Now().Hour(),
		Endpoint:        r.URL.Path,
		Protocol:        ProtocolOpenAI,
		AllowedRuleIDs:  allowedRuleIDs,
	}
	logEntry.InputTokens = inbound.EstimatedTokens
	p.insertPendingLog(logEntry)

	candidates, err := p.resolveCandidates(inbound)
	if err != nil {
		slog.Warn("proxy: no candidates", "path", r.URL.Path, "model", inbound.Model, "err", err)
		errType := "service_unavailable"
		if errors.Is(err, errNoMatch) {
			errType = "no_matching_rule"
		}
		p.writeError(w, http.StatusServiceUnavailable, errType, err.Error())
		logEntry.StatusCode = http.StatusServiceUnavailable
		logEntry.Error = err.Error()
		return
	}

	p.forwardWithFailover(w, r, body, candidates, false, inbound.EstimatedTokens, logEntry)
}

// handleModels returns the client-facing list of model names. The list is
// driven by enabled model rules (each rule's Name is the model name) rather
// than by the upstream lookup table, so /v1/models only shows models that
// the operator has explicitly exposed through a rule.
func (p *Proxy) handleModels(w http.ResponseWriter, r *http.Request) {
	_, _, allowedRuleIDs, authenticated, authErr := p.authenticate(r)
	if authErr != nil {
		p.writeError(w, http.StatusInternalServerError, "internal_error", "Failed to authenticate API key")
		return
	}
	rules, err := p.store.ListModelRules()
	if err != nil {
		slog.Error("proxy: list models failed", "err", err)
		p.writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list models")
		return
	}
	if authenticated && len(allowedRuleIDs) > 0 {
		allowed := make(map[string]struct{}, len(allowedRuleIDs))
		for _, id := range allowedRuleIDs {
			allowed[id] = struct{}{}
		}
		filtered := rules[:0]
		for _, rule := range rules {
			if _, ok := allowed[rule.ID]; ok {
				filtered = append(filtered, rule)
			}
		}
		rules = filtered
	}
	data := make([]modelItem, 0, len(rules))
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		if rule.Name == "" {
			continue
		}
		if _, ok := seen[rule.Name]; ok {
			continue
		}
		seen[rule.Name] = struct{}{}
		data = append(data, modelItem{ID: rule.Name, Object: "model"})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"object": "list",
		"data":   data,
	})
}

func (p *Proxy) handleTokenStats(w http.ResponseWriter, r *http.Request) {
	_, _, _, ok, err := p.authenticate(r)
	if err != nil || !ok {
		status := http.StatusUnauthorized
		if err != nil {
			status = http.StatusInternalServerError
			slog.Error("proxy: auth failed (internal error)", "path", r.URL.Path, "err", err)
		} else {
			slog.Warn("proxy: auth failed (invalid API key)", "path", r.URL.Path)
		}
		p.writeError(w, status, "invalid_request_error", "Invalid API key")
		return
	}

	dash, err := p.store.Dashboard()
	if err != nil {
		slog.Error("proxy: get stats failed", "err", err)
		p.writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get stats")
		return
	}
	var todayTokens int64
	var todayCost float64
	if len(dash.TokenTrend) > 0 {
		last := dash.TokenTrend[len(dash.TokenTrend)-1]
		todayTokens = last.InputTokens + last.OutputTokens
		todayCost = last.Cost
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"today_tokens": todayTokens,
		"today_cost":   todayCost,
	})
}

func (p *Proxy) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "ok",
		"autoapi": "v0.5.1",
		"proxy":   "running",
	})
}

func (p *Proxy) handleNotFound(w http.ResponseWriter, r *http.Request) {
	slog.Warn("proxy: unknown endpoint", "path", r.URL.Path, "method", r.Method)
	p.writeError(w, http.StatusNotFound, "invalid_request_error", "Invalid endpoint")
}

// rewriteBodyModel sets the "model" field in the JSON body. If the body is not
// valid JSON or modelName is empty, it returns the original body unchanged.
func rewriteBodyModel(body []byte, modelName string) ([]byte, error) {
	if modelName == "" {
		return body, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return body, nil
	}
	if raw, ok := m["model"]; ok {
		var current string
		if json.Unmarshal(raw, &current) == nil && current == modelName {
			return body, nil
		}
	}
	encoded, err := json.Marshal(modelName)
	if err != nil {
		return body, err
	}
	m["model"] = json.RawMessage(encoded)
	return json.Marshal(m)
}

// extractHeaders builds a simple case-sensitive map of select headers used by
// the matcher. The matcher itself normalizes the X-Priority lookup.
func extractHeaders(h http.Header) map[string]string {
	out := make(map[string]string)
	for k, v := range h {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}

// isMultipartRequest reports whether the request body is multipart/form-data.
// The /v1/audio/transcriptions and /v1/files endpoints use multipart encoding
// so they can carry binary payloads (audio files, document uploads).
func isMultipartRequest(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	return strings.HasPrefix(ct, "multipart/form-data")
}

// extractMultipartModel reads the "model" field from a multipart/form-data
// body without consuming it. The body has already been buffered into a byte
// slice by the handler, so we reconstruct a reader and use mime/multipart to
// parse just the form fields (ignoring file content). Returns "" if the body
// cannot be parsed or the model field is absent.
func extractMultipartModel(body []byte, r *http.Request) string {
	//mime.ParseMediaType extracts boundary=xxxx from the Content-Type header.
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return ""
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ""
		}
		if part.FormName() == "model" {
			val, err := io.ReadAll(io.LimitReader(part, 1024))
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(val))
		}
	}
	return ""
}
