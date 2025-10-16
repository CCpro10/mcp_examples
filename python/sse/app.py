"""Simple SSE server exposing MCP-compatible JSON-RPC endpoints."""

from __future__ import annotations

import asyncio
import json
import uuid
from typing import Dict, Optional

from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import JSONResponse, StreamingResponse

app = FastAPI(title="MCP SSE Example", version="0.1.0")


class SessionRegistry:
    """Thread-safe registry for active SSE sessions."""

    def __init__(self) -> None:
        self._sessions: Dict[str, asyncio.Queue[str]] = {}
        self._lock = asyncio.Lock()

    async def create(self, session_id: str) -> asyncio.Queue[str]:
        queue: asyncio.Queue[str] = asyncio.Queue()
        async with self._lock:
            self._sessions[session_id] = queue
        return queue

    async def pop(self, session_id: str) -> Optional[asyncio.Queue[str]]:
        async with self._lock:
            return self._sessions.pop(session_id, None)

    async def get(self, session_id: str) -> asyncio.Queue[str]:
        async with self._lock:
            queue = self._sessions.get(session_id)
        if queue is None:
            raise KeyError(session_id)
        return queue


sessions = SessionRegistry()


def format_sse(event: str, data: str) -> str:
    return f"event: {event}\ndata: {data}\n\n"


@app.get("/sse", name="sse_stream")
async def sse_stream(request: Request) -> StreamingResponse:
    session_id = uuid.uuid4().hex
    queue = await sessions.create(session_id)

    async def event_generator() -> asyncio.AsyncIterator[str]:
        try:
            endpoint_url = str(request.url_for("post_message")) + f"?sessionid={session_id}"
        except Exception:
            endpoint_url = f"/message?sessionid={session_id}"

        # Send endpoint metadata first so the client knows how to POST results.
        await queue.put(format_sse("endpoint", endpoint_url))

        try:
            while True:
                data = await queue.get()
                yield data
        except asyncio.CancelledError:
            raise
        finally:
            await sessions.pop(session_id)

    return StreamingResponse(
        event_generator(),
        media_type="text/event-stream",
        headers={"Cache-Control": "no-cache", "Connection": "keep-alive"},
    )


@app.post("/message", name="post_message")
async def post_message(request: Request) -> JSONResponse:
    session_id = request.query_params.get("sessionid")
    if not session_id:
        raise HTTPException(status_code=400, detail="sessionid must be provided for POST requests")

    try:
        queue = await sessions.get(session_id)
    except KeyError:
        raise HTTPException(status_code=404, detail="session not found; establish the SSE stream first")

    payload = await request.json()
    response = build_response(payload)

    await queue.put(format_sse("message", json.dumps(response)))
    return JSONResponse(status_code=202, content={"status": "accepted"})


def build_response(request_payload: dict) -> dict:
    method = request_payload.get("method")
    request_id = request_payload.get("id")

    if method == "initialize":
        result = {
            "protocolVersion": "2025-03-26",
            "serverInfo": {"name": "Python To-Uppercase Server", "version": "1.0.0"},
            "capabilities": {"tools": {"listChanged": True}},
        }
        return {"jsonrpc": "2.0", "id": request_id, "result": result}

    if method == "tools/list":
        result = {
            "tools": [
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
                }
            ]
        }
        return {"jsonrpc": "2.0", "id": request_id, "result": result}

    if method == "tools/call":
        params = request_payload.get("params", {})
        if params.get("name") == "to-uppercase":
            arguments = params.get("arguments", {})
            input_value = arguments.get("input", "")
            upper = perform_to_uppercase(input_value)
            result = {
                "content": [{"type": "text", "text": upper}],
                "isError": False,
            }
            return {"jsonrpc": "2.0", "id": request_id, "result": result}

    return {
        "jsonrpc": "2.0",
        "id": request_id,
        "error": {"code": -32601, "message": "Method not found"},
    }


def perform_to_uppercase(text: str) -> str:
    return text.upper()


if __name__ == "__main__":
    import uvicorn

    uvicorn.run("app:app", host="0.0.0.0", port=8080, reload=False)
