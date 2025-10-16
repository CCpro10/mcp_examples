package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
)

const (
	pathSSE     = "/sse"
	pathMessage = "/message"

	eventEndpoint = "endpoint"
	eventMessage  = "message"
)

// JSONRPCRequest models a JSON-RPC request payload.
type JSONRPCRequest struct {
	JSONRPC string                 `json:"jsonrpc"`
	Method  string                 `json:"method"`
	Params  map[string]interface{} `json:"params,omitempty"`
	ID      interface{}            `json:"id,omitempty"`
}

// JSONRPCResponse models a JSON-RPC response payload.
type JSONRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      interface{}   `json:"id,omitempty"`
	Result  interface{}   `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"`
}

// JSONRPCError represents a JSON-RPC error object.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// InitializeResult is returned by the "initialize" method.
type InitializeResult struct {
	ProtocolVersion string                 `json:"protocolVersion"`
	ServerInfo      map[string]string      `json:"serverInfo"`
	Capabilities    map[string]interface{} `json:"capabilities"`
}

// ToolsListResult lists available tools.
type ToolsListResult struct {
	Tools []Tool `json:"tools"`
}

// ToolResult holds the payload for "tools/call".
type ToolResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError"`
}

// ContentBlock represents the content section inside ToolResult.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Tool describes a single tool entry.
type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"inputSchema"`
}

// InputSchema defines tool arguments.
type InputSchema struct {
	Type       string                 `json:"type"`
	Properties map[string]PropertyDef `json:"properties"`
	Required   []string               `json:"required"`
}

// PropertyDef specifies a single input argument.
type PropertyDef struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

var toUppercaseTool = Tool{
	Name:        "to-uppercase",
	Description: "Converts the input string to uppercase.",
	InputSchema: InputSchema{
		Type: "object",
		Properties: map[string]PropertyDef{
			"input": {
				Type:        "string",
				Description: "The string to be converted to uppercase.",
			},
		},
		Required: []string{"input"},
	},
}

// session retains an active SSE connection.
type session struct {
	response http.ResponseWriter
	flusher  http.Flusher
}

// sseHandler manages lifecycle of SSE sessions.
type sseHandler struct {
	sessions sync.Map // map[string]*session
}

func newSSEHandler() *sseHandler {
	return &sseHandler{}
}

func (h *sseHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == pathSSE:
		h.handleStream(w, r)
	case r.Method == http.MethodPost && r.URL.Path == pathMessage:
		h.handleMessage(w, r)
	default:
		jsonError(w, "Method or Path not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *sseHandler) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	sid := generateSessionID()
	h.sessions.Store(sid, &session{response: w, flusher: flusher})
	defer h.sessions.Delete(sid)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	endpointURL := fmt.Sprintf("%s://%s%s?sessionid=%s", schemeFromRequest(r), r.Host, pathMessage, sid)
	sendEvent(w, flusher, eventEndpoint, endpointURL)

	<-r.Context().Done()
}

func (h *sseHandler) handleMessage(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("sessionid")
	if sid == "" {
		jsonError(w, "sessionid must be provided for POST requests", http.StatusBadRequest)
		return
	}

	value, ok := h.sessions.Load(sid)
	if !ok {
		jsonError(w, "session not found, establish SSE stream first", http.StatusNotFound)
		return
	}

	payload, err := io.ReadAll(r.Body)
	if err != nil {
		jsonError(w, "cannot read request body", http.StatusInternalServerError)
		return
	}

	var req JSONRPCRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		jsonError(w, "failed to parse body", http.StatusBadRequest)
		return
	}

	go h.respond(value.(*session), sid, req)
	w.WriteHeader(http.StatusAccepted)
}

func (h *sseHandler) respond(s *session, sid string, req JSONRPCRequest) {
	resp := buildResponse(req)
	data, err := json.Marshal(resp)
	if err != nil {
		log.Printf("marshal response failed: %v", err)
		return
	}

	current, ok := h.sessions.Load(sid)
	if !ok {
		log.Printf("session %s closed before response was sent", sid)
		return
	}

	sendEvent(current.(*session).response, current.(*session).flusher, eventMessage, string(data))
}

func buildResponse(req JSONRPCRequest) *JSONRPCResponse {
	switch req.Method {
	case "initialize":
		result := InitializeResult{
			ProtocolVersion: "2025-03-26",
			ServerInfo: map[string]string{
				"name":    "Go To-Uppercase Server",
				"version": "12.0.0",
			},
			Capabilities: map[string]interface{}{
				"tools": map[string]interface{}{"listChanged": true},
			},
		}
		return &JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: result}

	case "tools/list":
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  ToolsListResult{Tools: []Tool{toUppercaseTool}},
		}

	case "tools/call":
		if req.Params == nil || req.Params["name"] != "to-uppercase" {
			return newErrorResponse(req.ID, -32602, "Unsupported tool name")
		}
		args, _ := req.Params["arguments"].(map[string]interface{})
		input, _ := args["input"].(string)
		upper, err := performToUppercase(input)
		if err != nil {
			return newErrorResponse(req.ID, -32602, err.Error())
		}
		result := ToolResult{
			Content: []ContentBlock{{Type: "text", Text: upper}},
			IsError: false,
		}
		return &JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
	}

	return newErrorResponse(req.ID, -32601, "Method not found")
}

func newErrorResponse(id interface{}, code int, message string) *JSONRPCResponse {
	return &JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &JSONRPCError{
			Code:    code,
			Message: message,
		},
	}
}

func sendEvent(w http.ResponseWriter, flusher http.Flusher, name, data string) {
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data); err != nil {
		log.Printf("send event failed: %v", err)
		return
	}
	flusher.Flush()
}

func performToUppercase(input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", fmt.Errorf("input string cannot be empty")
	}
	return strings.ToUpper(input), nil
}

func jsonError(w http.ResponseWriter, message string, code int) {
	log.Printf("sending error response: %s", message)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func generateSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		log.Printf("generate session id failed: %v", err)
		return ""
	}
	return hex.EncodeToString(b)
}

func schemeFromRequest(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if scheme := r.Header.Get("X-Forwarded-Proto"); scheme != "" {
		return scheme
	}
	return "http"
}

func main() {
	http.Handle("/", newSSEHandler())
	log.Println("SSE MCP server listening on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
