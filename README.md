# MCP Examples

示例仓库同时包含 Go 与 Python 实现的 MCP SSE 与 Streamable HTTP 服务，便于对照学习不同语言下的实现方式。

## 目录结构

```
.
├── go/                 # Go 版本示例
│   ├── go.mod
│   ├── sse/            # 事件流 (SSE) 服务 `go run ./go/sse`
│   └── streamable/     # JSON-RPC Streamable 服务 `go run ./go/streamable`
├── python/             # Python 版本示例
│   ├── requirements.txt
│   ├── sse/app.py      # `python python/sse/app.py` 或 `uvicorn python.sse.app:app`
│   └── streamable/app.py
└── legacy/             # 旧的演示代码，暂存备用
```

## Go 示例

### 构建 / 运行

```bash
cd go
# 编译检查
go build ./...

# 启动 SSE 服务（默认端口 8080）
go run ./sse

# 启动 Streamable 服务（默认端口 9090）
go run ./streamable
```

两个服务都实现了 `initialize`、`tools/list` 以及 `tools/call` (`to-uppercase`、`to-uppercase-slowly`) 等 MCP 常见接口。其中 `streamable` 示例会根据请求自动切换到 SSE 输出，向客户端推送进度通知。

## Python 示例

### 安装依赖

```bash
pip install -r python/requirements.txt
```

### 启动 SSE 服务

```bash
python python/sse/app.py
# 或
uvicorn python.sse.app:app --host 0.0.0.0 --port 8080
```

### 启动 Streamable 服务

```bash
python python/streamable/app.py
# 或
uvicorn python.streamable.app:app --host 0.0.0.0 --port 9090
```

FastAPI 实现与 Go 版本保持一致：

- `/sse`：创建事件流并返回 `sessionId` 对应的 `/message` 入口。
- `/message`：接收 MCP JSON-RPC 请求并通过已建立的 SSE 连接返回结果。
- `/mcp`：根据方法自动返回 JSON 或升级为 SSE 推送进度。

## 请求示例

### 初始化

```bash
curl -X POST http://localhost:9090/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize"}'
```

### 慢速大写 (Streamable)

```bash
curl -N -X POST http://localhost:9090/mcp \
  -H 'Content-Type: application/json' \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "tools/call",
    "params": {
      "name": "to-uppercase-slowly",
      "arguments": {"input": "hello"},
      "_meta": {"progressToken": "token-1"}
    }
  }'
```

客户端会持续收到 `notifications/progress` 事件，直至最终结果。

---

如需扩展新的示例，可在 `go/` 与 `python/` 目录下新增独立子目录并在 README 中补充说明。
