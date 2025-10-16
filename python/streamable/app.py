"""Streamable HTTP server implementing JSON-RPC over SSE-like responses."""

from __future__ import annotations

import asyncio
import json
from typing import Any, AsyncIterator, Dict

from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import JSONResponse, StreamingResponse

app = FastAPI(title="MCP Streamable Example", version="0.1.0")


TOOLS = [
    {
        "name": "to-uppercase",
        "description": "Converts the input string to uppercase.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "input": {
                    "type": "string",
                    "description": "The string to be converted to uppercase.",
                }
            },
            "required": ["input"],
        },
    },
    {
        "name": "to-uppercase-slowly",
        "description": "Converts the input string to uppercase. (simulates slow processing)",
        "inputSchema": {
            "type": "object",
            "properties": {
                "input": {
                    "type": "string",
                    "description": "The string to be converted to uppercase.",
                }
            },
            "required": ["input"],
        },
    },
]


def format_sse(event: str, data: str) -> str:
    return f"event: {event}\ndata: {data}\n\n"


def base_response(result: Dict[str, Any] | None = None, error: Dict[str, Any] | None = None, *, request_id: Any = None) -> Dict[str, Any]:
    response: Dict[str, Any] = {"jsonrpc": "2.0", "id": request_id}
    if error is not None:
        response["error"] = error
    else:
        response["result"] = result
    return response


@app.post("/mcp")
async def handle_mcp(request: Request):
    payload = await request.json()
    method = payload.get("method")
    request_id = payload.get("id")

    if method == "initialize":
        result = {
            "protocolVersion": "2025-03-26",
            "serverInfo": {"name": "Python To-Uppercase Server", "version": "1.0.0"},
            "capabilities": {"tools": {"listChanged": True}},
        }
        return JSONResponse(content=base_response(result=result, request_id=request_id))

    if method == "tools/list":
        return JSONResponse(content=base_response(result={"tools": TOOLS}, request_id=request_id))

    if method == "tools/call":
        params = payload.get("params", {})
        tool_name = params.get("name")
        arguments = params.get("arguments", {})
        input_value = str(arguments.get("input", ""))

        if tool_name == "to-uppercase":
            result = {
                "content": [{"type": "text", "text": input_value.upper()}],
                "isError": False,
            }
            return JSONResponse(content=base_response(result=result, request_id=request_id))

        if tool_name == "to-uppercase-slowly":
            return StreamingResponse(
                slow_uppercase_stream(payload, request_id, input_value),
                media_type="text/event-stream",
                headers={"Cache-Control": "no-cache", "Connection": "keep-alive"},
            )

        error = {"code": -32602, "message": "Unsupported tool name"}
        return JSONResponse(content=base_response(error=error, request_id=request_id))

    error = {"code": -32601, "message": "Method not found"}
    return JSONResponse(content=base_response(error=error, request_id=request_id))


async def slow_uppercase_stream(payload: Dict[str, Any], request_id: Any, input_value: str) -> AsyncIterator[str]:
    params = payload.get("params", {})
    meta = params.get("_meta", {})
    progress_token = meta.get("progressToken")

    for i in range(1, 11):
        event_payload = {
            "jsonrpc": "2.0",
            "method": "notifications/progress",
            "params": {
                "progress": i,
                "total": 10,
                "progressToken": progress_token,
                "message": f"Server progress {int(i * 100 / 10)}%",
            },
        }
        yield format_sse("message", json.dumps(event_payload))
        await asyncio.sleep(0.3)

    result = {
        "content": [{"type": "text", "text": input_value.upper()}],
        "isError": False,
    }
    final_response = base_response(result=result, request_id=request_id)
    yield format_sse("message", json.dumps(final_response))


if __name__ == "__main__":
    import uvicorn

    uvicorn.run("app:app", host="0.0.0.0", port=9090, reload=False)
