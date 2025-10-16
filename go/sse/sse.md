
# Go SSE MCP 服务解析

`go/sse/main.go` 演示了如何通过 Server-Sent Events (SSE) 与 MCP 客户端进行双向交互。服务监听 `:8080`，核心交互路径有两个：

- `GET /sse`：建立 SSE 长连接并返回一个 `sessionid`。
- `POST /message?sessionid=xxx`：接收 JSON-RPC 请求，服务端通过先前的 SSE 连接推送响应。

## 核心结构

```go
type session struct {
    response http.ResponseWriter
    flusher  http.Flusher
}

type sseHandler struct {
    sessions sync.Map // map[string]*session
}
```

`sseHandler` 将 `sessionid` 到活跃连接的映射存放在 `sync.Map` 中，便于并发访问。

## 入口路由

```go
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
```

- `handleStream`：负责建立长连接并写入首条 `event:endpoint`，告知客户端 POST URL。
- `handleMessage`：读取 JSON-RPC 请求并异步发送结果。

## 建立 SSE 连接

```go
func (h *sseHandler) handleStream(w http.ResponseWriter, r *http.Request) {
    flusher, ok := w.(http.Flusher)
    ...
    sid := generateSessionID()
    h.sessions.Store(sid, &session{response: w, flusher: flusher})
    defer h.sessions.Delete(sid)

    w.Header().Set("Content-Type", "text/event-stream")
    ...
    endpointURL := fmt.Sprintf("%s://%s%s?sessionid=%s", schemeFromRequest(r), r.Host, pathMessage, sid)
    sendEvent(w, flusher, eventEndpoint, endpointURL)

    <-r.Context().Done()
}
```

- 成功建立 SSE 后立即推送 `endpoint` 事件，客户端由此得知后续 POST 的目标。
- `defer` 清理会话，避免资源泄露。

## 处理 JSON-RPC 请求

```go
func (h *sseHandler) handleMessage(w http.ResponseWriter, r *http.Request) {
    sid := r.URL.Query().Get("sessionid")
    ...
    payload, _ := io.ReadAll(r.Body)
    var req JSONRPCRequest
    json.Unmarshal(payload, &req)

    go h.respond(value.(*session), sid, req)
    w.WriteHeader(http.StatusAccepted)
}
```

- 找不到 `sessionid` 将返回 404，提醒客户端先调用 `GET /sse`。
- 使用 goroutine 异步处理，保证 POST 快速响应 202。

```go
func (h *sseHandler) respond(s *session, sid string, req JSONRPCRequest) {
    resp := buildResponse(req)
    data, _ := json.Marshal(resp)

    current, ok := h.sessions.Load(sid)
    if !ok {
        log.Printf("session %s closed before response was sent", sid)
        return
    }

    sendEvent(current.(*session).response, current.(*session).flusher, eventMessage, string(data))
}
```

## JSON-RPC 分发

```go
func buildResponse(req JSONRPCRequest) *JSONRPCResponse {
    switch req.Method {
    case "initialize": ...
    case "tools/list": ...
    case "tools/call":
        if req.Params["name"] != "to-uppercase" {
            return newErrorResponse(...)
        }
        upper, err := performToUppercase(input)
        ...
    }
    return newErrorResponse(req.ID, -32601, "Method not found")
}
```

- 仅提供一个 `to-uppercase` 工具；参数缺失或字符串为空会返回 JSON-RPC 错误对象。
- 所有响应（包括错误）都会通过 SSE 推送 `event: message`。

## 发送事件

```go
func sendEvent(w http.ResponseWriter, flusher http.Flusher, name, data string) {
    fmt.Fprintf(w, "event: %s
data: %s

", name, data)
    flusher.Flush()
}
```

只要连接仍有效，服务端可多次调用 `sendEvent` 推送后续消息。客户端侧只需维护一个 SSE 连接即可实时接收响应。

---

**流程总结**

1. 客户端调用 `GET /sse`，拿到 `sessionid`。
2. 后续所有 JSON-RPC 请求都以 `POST /message?sessionid=...` 方式提交。
3. 服务端异步生成结果，并通过 SSE 将最终响应（或错误）发送给对应会话。

这样，长连接负责单向推送，短连接负责提交请求，实现了 MCP 场景下的轻量双通道通信。
