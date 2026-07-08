// handlers.go registers the OpenAI-compatible endpoints on the chi router.
//
// Streaming note (Phase 4 v1): response bodies are buffered in full inside
// ModifyResponse before being released to the client. This sacrifices real-time
// streaming UX for reliable usage parsing and request logging. A future phase
// can replace this with a teeing ResponseWriter that parses usage on the fly.
package proxy

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"time"

	"autoapi/internal/model"

	"github.com/go-chi/chi/v5/middleware"
)

const maxBodySize = 10 << 20 // 10 MiB

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

	apiKeyID, ok, err := p.authenticate(r)
	if err != nil || !ok {
		status := http.StatusUnauthorized
		if err != nil {
			status = http.StatusInternalServerError
		}
		p.writeError(w, status, "invalid_request_error", "Invalid API key")
		logEntry.StatusCode = status
		logEntry.APIKeyID = apiKeyID
		logEntry.Error = "authentication failed"
		return
	}
	logEntry.APIKeyID = apiKeyID

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodySize))
	if err != nil {
		p.writeError(w, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		logEntry.StatusCode = http.StatusBadRequest
		logEntry.Error = err.Error()
		return
	}
	_ = r.Body.Close()

	var chatReq chatRequest
	if err := json.Unmarshal(body, &chatReq); err != nil {
		p.writeError(w, http.StatusBadRequest, "invalid_request_error", "Invalid JSON body")
		logEntry.StatusCode = http.StatusBadRequest
		logEntry.Error = err.Error()
		return
	}
	if chatReq.Task == "" {
		chatReq.Task = "chat"
	}

	inbound := &InboundRequest{
		Model:           chatReq.Model,
		Header:          extractHeaders(r.Header),
		EstimatedTokens: estimateChatInputTokens(len(chatReq.Messages)),
		Task:            chatReq.Task,
		TimeHour:        time.Now().Hour(),
	}

	candidates, err := p.resolveCandidates(inbound)
	if err != nil {
		p.writeError(w, http.StatusServiceUnavailable, "service_unavailable", err.Error())
		logEntry.StatusCode = http.StatusServiceUnavailable
		logEntry.Error = err.Error()
		return
	}

	p.forwardWithFailover(w, r, body, candidates, chatReq.Stream, inbound.EstimatedTokens, logEntry)
	logEntry.LatencyMs = int(time.Since(start).Milliseconds())
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

	apiKeyID, ok, err := p.authenticate(r)
	if err != nil || !ok {
		status := http.StatusUnauthorized
		if err != nil {
			status = http.StatusInternalServerError
		}
		p.writeError(w, status, "invalid_request_error", "Invalid API key")
		logEntry.StatusCode = status
		logEntry.APIKeyID = apiKeyID
		logEntry.Error = "authentication failed"
		return
	}
	logEntry.APIKeyID = apiKeyID

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodySize))
	if err != nil {
		p.writeError(w, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		logEntry.StatusCode = http.StatusBadRequest
		logEntry.Error = err.Error()
		return
	}
	_ = r.Body.Close()

	var embReq embeddingsRequest
	if err := json.Unmarshal(body, &embReq); err != nil {
		p.writeError(w, http.StatusBadRequest, "invalid_request_error", "Invalid JSON body")
		logEntry.StatusCode = http.StatusBadRequest
		logEntry.Error = err.Error()
		return
	}

	inbound := &InboundRequest{
		Model:           embReq.Model,
		Header:          extractHeaders(r.Header),
		EstimatedTokens: estimateEmbeddingInputTokens(embReq.Input) / 4,
		Task:            "embeddings",
		TimeHour:        time.Now().Hour(),
	}

	candidates, err := p.resolveCandidates(inbound)
	if err != nil {
		p.writeError(w, http.StatusServiceUnavailable, "service_unavailable", err.Error())
		logEntry.StatusCode = http.StatusServiceUnavailable
		logEntry.Error = err.Error()
		return
	}

	p.forwardWithFailover(w, r, body, candidates, false, inbound.EstimatedTokens, logEntry)
	logEntry.LatencyMs = int(time.Since(start).Milliseconds())
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

	apiKeyID, ok, err := p.authenticate(r)
	if err != nil || !ok {
		status := http.StatusUnauthorized
		if err != nil {
			status = http.StatusInternalServerError
		}
		p.writeError(w, status, "invalid_request_error", "Invalid API key")
		logEntry.StatusCode = status
		logEntry.APIKeyID = apiKeyID
		logEntry.Error = "authentication failed"
		return
	}
	logEntry.APIKeyID = apiKeyID

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodySize))
	if err != nil {
		p.writeError(w, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		logEntry.StatusCode = http.StatusBadRequest
		logEntry.Error = err.Error()
		return
	}
	_ = r.Body.Close()

	var genericReq genericOpenAIRequest
	_ = json.Unmarshal(body, &genericReq)

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
	}

	candidates, err := p.resolveCandidates(inbound)
	if err != nil {
		p.writeError(w, http.StatusServiceUnavailable, "service_unavailable", err.Error())
		logEntry.StatusCode = http.StatusServiceUnavailable
		logEntry.Error = err.Error()
		return
	}

	p.forwardWithFailover(w, r, body, candidates, false, inbound.EstimatedTokens, logEntry)
	logEntry.LatencyMs = int(time.Since(start).Milliseconds())
}

// handleModels returns the client-facing list of model names. The list is
// driven by enabled model rules (each rule's Name is the model name) rather
// than by the upstream lookup table, so /v1/models only shows models that
// the operator has explicitly exposed through a rule.
func (p *Proxy) handleModels(w http.ResponseWriter, r *http.Request) {
	rules, err := p.store.ListModelRules()
	if err != nil {
		p.writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list models")
		return
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
	_, ok, err := p.authenticate(r)
	if err != nil || !ok {
		status := http.StatusUnauthorized
		if err != nil {
			status = http.StatusInternalServerError
		}
		p.writeError(w, status, "invalid_request_error", "Invalid API key")
		return
	}

	dash, err := p.store.Dashboard()
	if err != nil {
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
		"autoapi": "v0.4.2",
		"proxy":   "running",
	})
}

func (p *Proxy) handleNotFound(w http.ResponseWriter, r *http.Request) {
	p.writeError(w, http.StatusNotFound, "invalid_request_error", "Invalid endpoint")
}

// rewriteBodyModel sets the "model" field in the JSON body. If the body is not
// valid JSON or modelName is empty, it returns the original body unchanged.
func rewriteBodyModel(body []byte, modelName string) ([]byte, error) {
	if modelName == "" {
		return body, nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(body, &m); err != nil {
		return body, nil
	}
	m["model"] = modelName
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
