#!/usr/bin/env python3
"""MCP server for CyberStrikeAI container fleet.

ENV VARS required:
  CSAI_URL      - CyberStrikeAI base URL, e.g. http://localhost:8080
  CSAI_PASSWORD - CyberStrikeAI operator password (same as web UI login)
  GS_NETCAT_BIN - path to gs-netcat binary (default: gs-netcat)
"""
import asyncio, json, os, secrets, time
import httpx
from mcp.server import Server
from mcp.server.stdio import stdio_server
from mcp import types

CSAI_URL  = os.environ["CSAI_URL"].rstrip("/")
CSAI_PASS = os.environ["CSAI_PASSWORD"]
GS_BIN    = os.environ.get("GS_NETCAT_BIN", "gs-netcat")

server = Server("gs-panel")

# ── auth token cache ──────────────────────────────────────────────────────────

_token: str = ""
_token_expiry: float = 0.0


async def _get_token() -> str:
    global _token, _token_expiry
    if _token and time.time() < _token_expiry - 60:
        return _token
    async with httpx.AsyncClient(timeout=15) as c:
        r = await c.post(f"{CSAI_URL}/api/auth/login",
                         json={"password": CSAI_PASS})
        r.raise_for_status()
        data = r.json()
        _token = data["token"]
        # CyberStrikeAI sessions default to 24h; cache for 23h
        _token_expiry = time.time() + 23 * 3600
    return _token


async def _api(method: str, path: str, **kw):
    token = await _get_token()
    async with httpx.AsyncClient(
        timeout=30, headers={"Authorization": f"Bearer {token}"}
    ) as c:
        r = await c.request(method, f"{CSAI_URL}{path}", **kw)
        r.raise_for_status()
        return r.json()


# ── gsocket exec helper ───────────────────────────────────────────────────────

async def _gs_exec(secret: str, cmd: str, timeout: int = 60) -> dict:
    try:
        proc = await asyncio.create_subprocess_exec(
            GS_BIN, "-s", secret,
            stdin=asyncio.subprocess.PIPE,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        stdout, stderr = await asyncio.wait_for(
            proc.communicate(input=f"{cmd}\nexit\n".encode()),
            timeout=timeout,
        )
        return {
            "stdout": stdout.decode(errors="replace")[-8000:],
            "stderr": stderr.decode(errors="replace")[-2000:],
            "returncode": proc.returncode,
        }
    except asyncio.TimeoutError:
        return {"error": f"timeout after {timeout}s", "stdout": "", "returncode": -1}
    except Exception as e:
        return {"error": str(e), "stdout": "", "returncode": -1}


async def _resolve_host(host_id_or_name: str) -> dict:
    containers = await _api("GET", "/api/containers")
    for h in containers:
        if str(h["id"]) == host_id_or_name or host_id_or_name.lower() in h["name"].lower():
            return h
        if h.get("hostname") and host_id_or_name.lower() in h["hostname"].lower():
            return h
    names = [h["name"] for h in containers]
    raise ValueError(f"Container not found: {host_id_or_name!r} — available: {names}")


# ── tool definitions ──────────────────────────────────────────────────────────

@server.list_tools()
async def list_tools():
    return [
        types.Tool(
            name="list_containers",
            description="List all registered containers with ID, name, hostname, IP, and online status.",
            inputSchema={"type": "object", "properties": {}},
        ),
        types.Tool(
            name="exec",
            description="Run a shell command on a container via gsocket. Returns stdout/stderr/returncode.",
            inputSchema={
                "type": "object",
                "required": ["host", "cmd"],
                "properties": {
                    "host":    {"type": "string", "description": "Container ID, name, or hostname substring"},
                    "cmd":     {"type": "string", "description": "Shell command to run"},
                    "timeout": {"type": "integer", "description": "Timeout seconds (default 60, max 600)"},
                },
            },
        ),
        types.Tool(
            name="scan",
            description="Run nmap (default) or rustscan on a target from the container.",
            inputSchema={
                "type": "object",
                "required": ["host", "target"],
                "properties": {
                    "host":   {"type": "string"},
                    "target": {"type": "string", "description": "IP, hostname, or CIDR"},
                    "flags":  {"type": "string", "description": "nmap flags (default: -sT -sV --open -T4)"},
                    "fast":   {"type": "boolean", "description": "Use rustscan for fast port discovery first"},
                },
            },
        ),
        types.Tool(
            name="nuclei_scan",
            description="Run nuclei vulnerability scan against a target from the container.",
            inputSchema={
                "type": "object",
                "required": ["host", "target"],
                "properties": {
                    "host":      {"type": "string"},
                    "target":    {"type": "string"},
                    "templates": {"type": "string", "description": "Template tags (default: cves,exposures)"},
                    "severity":  {"type": "string", "description": "Severity filter: critical,high,medium,low"},
                },
            },
        ),
        types.Tool(
            name="upload_file",
            description="Upload a file (base64-encoded) to a container.",
            inputSchema={
                "type": "object",
                "required": ["host", "remote_path", "content_b64"],
                "properties": {
                    "host":        {"type": "string"},
                    "remote_path": {"type": "string", "description": "Destination path on container"},
                    "content_b64": {"type": "string", "description": "Base64-encoded file content"},
                },
            },
        ),
        types.Tool(
            name="download_file",
            description="Download a file from a container as base64-encoded content.",
            inputSchema={
                "type": "object",
                "required": ["host", "remote_path"],
                "properties": {
                    "host":        {"type": "string"},
                    "remote_path": {"type": "string"},
                },
            },
        ),
        types.Tool(
            name="deploy_container",
            description="Create a new container record with a fresh gsocket secret and return the docker run command.",
            inputSchema={
                "type": "object",
                "properties": {
                    "name":      {"type": "string", "description": "Container label (default: kali-cs)"},
                    "panel_url": {"type": "string", "description": "CyberStrikeAI URL for container callback"},
                },
            },
        ),
    ]


# ── tool dispatch ─────────────────────────────────────────────────────────────

@server.call_tool()
async def call_tool(name: str, arguments: dict):
    try:
        result = await _dispatch(name, arguments)
    except Exception as e:
        result = {"error": str(e)}
    return [types.TextContent(type="text", text=json.dumps(result, indent=2, ensure_ascii=False))]


async def _dispatch(name: str, args: dict):
    if name == "list_containers":
        containers = await _api("GET", "/api/containers")
        return {"containers": [
            {"id": c["id"], "name": c["name"],
             "hostname": c.get("hostname"), "ip": c.get("ipAddress"),
             "online": c.get("isOnline", False), "tags": c.get("tags")}
            for c in containers
        ]}

    elif name == "exec":
        h = await _resolve_host(args["host"])
        timeout = min(int(args.get("timeout", 60)), 600)
        return await _gs_exec(h["gsSecret"], args["cmd"], timeout)

    elif name == "scan":
        h = await _resolve_host(args["host"])
        flags = args.get("flags", "-sT -sV --open -T4")
        target = args["target"]
        if args.get("fast"):
            cmd = f"rustscan -a {target} --ulimit 5000 -- {flags} 2>/dev/null"
        else:
            cmd = f"nmap {flags} {target} 2>/dev/null"
        return await _gs_exec(h["gsSecret"], cmd, timeout=300)

    elif name == "nuclei_scan":
        h = await _resolve_host(args["host"])
        tags = args.get("templates", "cves,exposures")
        sev  = f"-severity {args['severity']}" if args.get("severity") else ""
        cmd  = f"nuclei -u {args['target']} -tags {tags} {sev} -silent -json 2>/dev/null"
        return await _gs_exec(h["gsSecret"], cmd, timeout=600)

    elif name == "upload_file":
        import base64
        h       = await _resolve_host(args["host"])
        content = base64.b64decode(args["content_b64"])
        b64     = base64.b64encode(content).decode()
        remote  = args["remote_path"]
        cmd     = f"printf '%s' '{b64}' | base64 -d > {remote} && echo uploaded"
        return await _gs_exec(h["gsSecret"], cmd)

    elif name == "download_file":
        h   = await _resolve_host(args["host"])
        res = await _gs_exec(h["gsSecret"], f"base64 {args['remote_path']} 2>/dev/null")
        return {"content_b64": res.get("stdout", "").strip(),
                "error": res.get("error"), "returncode": res.get("returncode")}

    elif name == "deploy_container":
        label     = args.get("name", "kali-cs")
        panel_url = args.get("panel_url", CSAI_URL)
        data = await _api("POST", "/api/containers", json={"name": label, "panelUrl": panel_url})
        return {
            "gs_secret":  data["container"]["gsSecret"],
            "id":         data["container"]["id"],
            "name":       label,
            "docker_run": data["dockerRun"],
            "note": "Run on the target host. Container auto-registers on boot.",
        }

    else:
        return {"error": f"Unknown tool: {name}"}


# ── entry point ───────────────────────────────────────────────────────────────

async def main():
    async with stdio_server() as (r, w):
        await server.run(r, w, server.create_initialization_options())


if __name__ == "__main__":
    asyncio.run(main())
