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
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"autoapi/internal/model"
)

const maxBodySize = 10 << 20 // 10 MiB

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
	logEntry := &model.RequestLog{Timestamp: start.UnixMilli()}
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

	provider, targetModel, routeID, routeLabel, err := p.resolveTarget(inbound)
	if err != nil {
		p.writeError(w, http.StatusServiceUnavailable, "service_unavailable", err.Error())
		logEntry.StatusCode = http.StatusServiceUnavailable
		logEntry.Error = err.Error()
		return
	}

	upstreamKey, err := p.service.ResolveAPIKey(provider.APIKeyID)
	if err != nil {
		p.writeError(w, http.StatusServiceUnavailable, "upstream_error", "Failed to resolve provider API key")
		logEntry.StatusCode = http.StatusServiceUnavailable
		logEntry.Error = err.Error()
		return
	}

	body, err = rewriteBodyModel(body, targetModel)
	if err != nil {
		p.writeError(w, http.StatusInternalServerError, "internal_error", "Failed to rewrite request body")
		logEntry.StatusCode = http.StatusInternalServerError
		logEntry.Error = err.Error()
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Del("Transfer-Encoding")
	if r.Header.Get("Content-Type") == "" {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))

	upstreamURL, err := url.Parse(strings.TrimSuffix(provider.BaseURL, "/") + "/v1/chat/completions")
	if err != nil {
		p.writeError(w, http.StatusInternalServerError, "internal_error", "Invalid provider base URL")
		logEntry.StatusCode = http.StatusInternalServerError
		logEntry.Error = err.Error()
		return
	}

	logEntry.ProviderID = provider.ID
	logEntry.ProviderName = provider.Name
	logEntry.Model = targetModel
	logEntry.RouteID = routeID
	logEntry.RouteLabel = routeLabel

	p.doForward(w, r, upstreamURL, upstreamKey, routeID, chatReq.Stream, inbound.EstimatedTokens, logEntry)
	logEntry.LatencyMs = int(time.Since(start).Milliseconds())
}

func (p *Proxy) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	logEntry := &model.RequestLog{Timestamp: start.UnixMilli()}
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

	provider, targetModel, routeID, routeLabel, err := p.resolveTarget(inbound)
	if err != nil {
		p.writeError(w, http.StatusServiceUnavailable, "service_unavailable", err.Error())
		logEntry.StatusCode = http.StatusServiceUnavailable
		logEntry.Error = err.Error()
		return
	}

	upstreamKey, err := p.service.ResolveAPIKey(provider.APIKeyID)
	if err != nil {
		p.writeError(w, http.StatusServiceUnavailable, "upstream_error", "Failed to resolve provider API key")
		logEntry.StatusCode = http.StatusServiceUnavailable
		logEntry.Error = err.Error()
		return
	}

	body, err = rewriteBodyModel(body, targetModel)
	if err != nil {
		p.writeError(w, http.StatusInternalServerError, "internal_error", "Failed to rewrite request body")
		logEntry.StatusCode = http.StatusInternalServerError
		logEntry.Error = err.Error()
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Del("Transfer-Encoding")
	if r.Header.Get("Content-Type") == "" {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))

	upstreamURL, err := url.Parse(strings.TrimSuffix(provider.BaseURL, "/") + "/v1/embeddings")
	if err != nil {
		p.writeError(w, http.StatusInternalServerError, "internal_error", "Invalid provider base URL")
		logEntry.StatusCode = http.StatusInternalServerError
		logEntry.Error = err.Error()
		return
	}

	logEntry.ProviderID = provider.ID
	logEntry.ProviderName = provider.Name
	logEntry.Model = targetModel
	logEntry.RouteID = routeID
	logEntry.RouteLabel = routeLabel

	p.doForward(w, r, upstreamURL, upstreamKey, routeID, false, inbound.EstimatedTokens, logEntry)
	logEntry.LatencyMs = int(time.Since(start).Milliseconds())
}

func (p *Proxy) handleModels(w http.ResponseWriter, r *http.Request) {
	models, err := p.store.ListModels("")
	if err != nil {
		p.writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list models")
		return
	}
	data := make([]modelItem, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, m := range models {
		if _, ok := seen[m.Name]; ok {
			continue
		}
		seen[m.Name] = struct{}{}
		data = append(data, modelItem{ID: m.Name, Object: "model"})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"object": "list",
		"data":   data,
	})
}

func (p *Proxy) handleTokenStats(w http.ResponseWriter, r *http.Request) {
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
// valid JSON, it returns the original body unchanged.
func rewriteBodyModel(body []byte, modelName string) ([]byte, error) {
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
