
# Go Streamable MCP 服务解析

`go/streamable/main.go` 展示了 MCP 在单一 HTTP 接口中同时支持同步返回与流式推送的实现方式。服务监听 `:9090`，所有请求均通过 `POST /mcp` 进入。

## 关键结构

```go
type streamContext struct {
    writer        http.ResponseWriter
    flusher       http.Flusher
    request       *JSONRPCRequest
    upgradedToSSE bool
}

type streamableHandler struct{}
```

- `streamContext` 随 `context.Context` 传递，既保存原始请求，又能在后续阶段写回响应。
- `upgradedToSSE` 标记该请求是否已经切换为 SSE。

## 路由入口

```go
func (h *streamableHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost || r.URL.Path != streamablePath {
        jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
        return
    }

    payload, _ := io.ReadAll(r.Body)
    var req JSONRPCRequest
    json.Unmarshal(payload, &req)

    ctx := context.WithValue(r.Context(), ctxKey{}, &streamContext{writer: w, flusher: w.(http.Flusher), request: &req})

    resp := dispatch(ctx, req)
    sc := streamFromContext(ctx)
    if sc.upgradedToSSE {
        sendEvent(ctx, eventProgress, toJSONString(resp))
        closeEventStream(ctx)
        return
    }

    sendJSON(ctx, resp)
}
```

- 统一在 `dispatch` 中处理 JSON-RPC 逻辑。
- 若期间触发流式输出，最终由 SSE 推送并关闭连接；否则以 JSON 一次性返回。

## JSON-RPC 分发

```go
func dispatch(ctx context.Context, req JSONRPCRequest) *JSONRPCResponse {
    switch req.Method {
    case "initialize": ...
    case "tools/list": ...
    case "tools/call":
        return handleToolCall(ctx, req)
    }
    return newErrorResponse(req.ID, -32601, "Method not found")
}
```

### 普通工具

```go
func handleToolCall(ctx context.Context, req JSONRPCRequest) *JSONRPCResponse {
    name := params["name"].(string)
    input := args["input"].(string)

    switch name {
    case "to-uppercase":
        upper := strings.ToUpper(input)
        return &JSONRPCResponse{Result: ToolResult{Content: ...}}
    case "to-uppercase-slowly":
        return streamSlowUppercase(ctx, req.ID, input)
    default:
        return newErrorResponse(...)
    }
}
```

- `to-uppercase` 直接返回 JSON；客户端获得普通 JSON-RPC 响应。
- `to-uppercase-slowly` 进入流式分支。

### 流式分支

```go
func streamSlowUppercase(ctx context.Context, id interface{}, input string) *JSONRPCResponse {
    upper := strings.ToUpper(input)
    sc := streamFromContext(ctx)
    progressToken := extractProgressToken(sc.request)

    for i := 1; i <= 10; i++ {
        payload := {"jsonrpc":"2.0", "method":"notifications/progress", ...}
        sendEvent(ctx, eventProgress, toJSONString(payload))
        time.Sleep(300 * time.Millisecond)
    }

    result := ToolResult{Content: []ContentBlock{{Type: "text", Text: upper}}}
    return &JSONRPCResponse{JSONRPC: "2.0", ID: id, Result: result}
}
```

- 每次循环通过 `sendEvent` 推送 `notifications/progress`，客户端实时收到进度。
- 循环结束后再返回最终 `result`，由 `ServeHTTP` 统一推送并关闭连接。

## SSE 升级与收尾

```go
func sendEvent(ctx context.Context, name, data string) {
    sc := streamFromContext(ctx)
    if !sc.upgradedToSSE {
        sc.upgradedToSSE = true
        sc.writer.Header().Set("Content-Type", "text/event-stream")
        ...
    }
    fmt.Fprintf(sc.writer, "event: %s
data: %s

", name, data)
    sc.flusher.Flush()
}
```

- 首次调用会写入 SSE 响应头并回写 `200 OK`。
- 后续调用仅输出事件，不再重复设置头部。
- `closeEventStream` 尝试 hijack 底层连接并关闭，确保客户端感知结束。

## 错误处理

所有错误都通过 `newErrorResponse` 封装为 JSON-RPC 错误对象，即便是在 SSE 流中也保持协议一致：

```go
func newErrorResponse(id interface{}, code int, message string) *JSONRPCResponse {
    return &JSONRPCResponse{
        JSONRPC: "2.0",
        ID:      id,
        Error:   &JSONRPCError{Code: code, Message: message},
    }
}
```

## 总结

1. 客户端始终 `POST /mcp`，无需区分同步/异步。
2. 服务端根据方法动态决定是否升级为 SSE。
3. 流式工具过程中推送进度与最终结果，非流式工具直接返回 JSON。

通过这样的设计，可以在 MCP 接口中兼顾传统 JSON-RPC 与 Streamable 能力，保持客户端协议的统一性。
