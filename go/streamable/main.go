package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

const (
	streamablePath = "/mcp"

	eventProgress = "message"
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

var (
	toUppercaseTool = Tool{
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

	toUppercaseSlowlyTool = Tool{
		Name:        "to-uppercase-slowly",
		Description: "Converts the input string to uppercase. (simulates slow processing)",
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
)

// streamContext stores request-scoped data for SSE responses.
type streamContext struct {
	writer        http.ResponseWriter
	flusher       http.Flusher
	request       *JSONRPCRequest
	upgradedToSSE bool
}

type ctxKey struct{}

type streamableHandler struct{}

func (h *streamableHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != streamablePath {
		jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
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

	ctx := context.WithValue(r.Context(), ctxKey{}, &streamContext{
		writer:  w,
		flusher: w.(http.Flusher),
		request: &req,
	})

	resp := dispatch(ctx, req)
	sc := streamFromContext(ctx)
	if sc.upgradedToSSE {
		sendEvent(ctx, eventProgress, toJSONString(resp))
		closeEventStream(ctx)
		return
	}

	sendJSON(ctx, resp)
}

func dispatch(ctx context.Context, req JSONRPCRequest) *JSONRPCResponse {
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
			Result:  ToolsListResult{Tools: []Tool{toUppercaseTool, toUppercaseSlowlyTool}},
		}

	case "tools/call":
		return handleToolCall(ctx, req)
	}

	return newErrorResponse(req.ID, -32601, "Method not found")
}

func handleToolCall(ctx context.Context, req JSONRPCRequest) *JSONRPCResponse {
	params := req.Params
	if params == nil {
		return newErrorResponse(req.ID, -32602, "Missing params")
	}

	name, _ := params["name"].(string)
	args, _ := params["arguments"].(map[string]interface{})
	input, _ := args["input"].(string)

	switch name {
	case "to-uppercase":
		upper, err := performToUppercase(input)
		if err != nil {
			return newErrorResponse(req.ID, -32602, err.Error())
		}
		result := ToolResult{
			Content: []ContentBlock{{Type: "text", Text: upper}},
			IsError: false,
		}
		return &JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: result}

	case "to-uppercase-slowly":
		return streamSlowUppercase(ctx, req.ID, input)
	default:
		return newErrorResponse(req.ID, -32602, "Unsupported tool name")
	}
}

func streamSlowUppercase(ctx context.Context, id interface{}, input string) *JSONRPCResponse {
	upper, err := performToUppercase(input)
	if err != nil {
		return newErrorResponse(id, -32602, err.Error())
	}

	sc := streamFromContext(ctx)
	progressToken := extractProgressToken(sc.request)

	for i := 1; i <= 10; i++ {
		payload := map[string]interface{}{
			"jsonrpc": "2.0",
			"method":  "notifications/progress",
			"params": map[string]interface{}{
				"progress":      i,
				"total":         10,
				"progressToken": progressToken,
				"message":       fmt.Sprintf("Server progress %d%%", i*10),
			},
		}
		sendEvent(ctx, eventProgress, toJSONString(payload))
		time.Sleep(300 * time.Millisecond)
	}

	result := ToolResult{
		Content: []ContentBlock{{Type: "text", Text: upper}},
		IsError: false,
	}
	return &JSONRPCResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func sendJSON(ctx context.Context, data interface{}) {
	sc := streamFromContext(ctx)
	sc.writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(sc.writer).Encode(data)
}

func sendEvent(ctx context.Context, name, data string) {
	sc := streamFromContext(ctx)

	if !sc.upgradedToSSE {
		sc.upgradedToSSE = true
		sc.writer.Header().Set("Content-Type", "text/event-stream")
		sc.writer.Header().Set("Connection", "keep-alive")
		sc.writer.Header().Set("Cache-Control", "no-cache")
		sc.writer.WriteHeader(http.StatusOK)
	}

	if _, err := fmt.Fprintf(sc.writer, "event: %s\ndata: %s\n\n", name, data); err != nil {
		log.Printf("send event failed: %v", err)
		return
	}
	sc.flusher.Flush()
}

func closeEventStream(ctx context.Context) {
	sc := streamFromContext(ctx)
	if sc == nil || !sc.upgradedToSSE {
		return
	}

	if hj, ok := sc.writer.(http.Hijacker); ok {
		conn, _, err := hj.Hijack()
		if err != nil {
			log.Printf("failed to hijack connection: %v", err)
			return
		}
		if err := conn.Close(); err != nil {
			log.Printf("failed to close SSE connection: %v", err)
		}
	}
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

func performToUppercase(input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", fmt.Errorf("input string cannot be empty")
	}
	return strings.ToUpper(input), nil
}

func extractProgressToken(req *JSONRPCRequest) interface{} {
	if req == nil || req.Params == nil {
		return nil
	}
	meta, _ := req.Params["_meta"].(map[string]interface{})
	if meta == nil {
		return nil
	}
	return meta["progressToken"]
}

func streamFromContext(ctx context.Context) *streamContext {
	sc, _ := ctx.Value(ctxKey{}).(*streamContext)
	return sc
}

func toJSONString(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("marshal payload failed: %v", err)
		return ""
	}
	return string(b)
}

func jsonError(w http.ResponseWriter, message string, code int) {
	log.Printf("sending error response: %s", message)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func main() {
	http.Handle(streamablePath, &streamableHandler{})
	log.Println("Streamable MCP server listening on http://localhost:9090")
	log.Fatal(http.ListenAndServe(":9090", nil))
}
