# Remote Container Fleet (Kali + gsocket)

CyberStrikeAI can deploy and control remote Kali Linux containers over
[gsocket](https://github.com/hackerschoice/gsocket) — no VPN, no port
forwarding, works through any NAT or firewall. The operator creates a container
record in the **Containers** tab of the web UI; CyberStrikeAI generates a
gsocket secret and a ready-to-run `docker run` command. Paste it on any Linux
host — the container calls home and becomes immediately controllable by the AI
agent and by a human via an interactive `gs-netcat` shell.

All container-related files live under `containers/`:

```
containers/
├── kali/
│   ├── Dockerfile       # Kali image with full recon/scan/bruteforce toolkit
│   └── entrypoint.sh    # Auto-registers with CyberStrikeAI, starts gsocket listener
└── panel-mcp/
    ├── mcp_server.py    # Stdio MCP server wrapping the CyberStrikeAI containers API
    └── requirements.txt
```

---

## Architecture

```
┌─────────────────────────────────────────────────────┐
│  Target host (anywhere)                             │
│  docker run cyberstrike/kali:latest                 │
│    ├── gs-netcat -l -s $GS_SECRET  ◄── gsocket relay │
│    └── POST /api/containers/register → CyberStrikeAI │
└─────────────────────────────────────────────────────┘
         │ gsocket relay (hackerschoice)
         ▼
┌─────────────────────────────────────────────────────┐
│  CyberStrikeAI (your server)                        │
│  SQLite containers table: id, name, gs_secret, …   │
│  REST API: GET/POST/DELETE /api/containers          │
│  Web UI: Containers tab (create, view, delete)      │
└─────────────────────────────────────────────────────┘
         │ REST API + gs-netcat exec
         ▼
┌─────────────────────────────────────────────────────┐
│  containers/panel-mcp/mcp_server.py  (stdio MCP)   │
│  AI tools: list_containers, exec, scan, nuclei_scan │
│            upload_file, download_file, deploy_container │
└─────────────────────────────────────────────────────┘
         │ MCP (external_mcp in config.yaml)
         ▼
┌─────────────────────────────────────────────────────┐
│  CyberStrikeAI agent                                │
└─────────────────────────────────────────────────────┘
```

gsocket relay is end-to-end encrypted. No inbound ports are required on the
container host. The gs_secret is stored in CyberStrikeAI's own SQLite DB —
no external panel service needed.

---

## Container toolkit

The Kali image installs via apt (kali-rolling repos):

| Category | Tools |
|---|---|
| Port scanning | nmap, rustscan, masscan, zmap |
| Web | sqlmap, ffuf, gobuster, nikto, dalfox, CRLFuzzer |
| Subdomain/DNS | subfinder, amass, sublist3r, dnsrecon, fierce |
| HTTP toolkit | httpx-toolkit, katana, gau, waybackurls |
| Vulnerability | nuclei (+ templates), paramspider |
| Brute force | hydra |
| SMB / Windows | netexec, impacket-scripts, smbmap, enum4linux-ng, rpcclient |
| Browser (AI) | playwright + chromium |
| Misc | responder, wordlists, python3, curl, jq, git |
| Connectivity | gsocket (gs-netcat) |

Image size is approximately 4–5 GB after build.

---

## Setup

### 1. Build the Kali image

On the host that will run containers, or build once and push to a private registry:

```bash
docker build -t cyberstrike/kali:latest containers/kali/
```

This takes 10–20 minutes on first build. Subsequent builds are cached.

### 2. Install gs-netcat on the CyberStrikeAI server

The MCP server calls `gs-netcat` locally to execute commands on containers.
Install the static binary:

```bash
# Linux x86_64
curl -sf https://api.github.com/repos/hackerschoice/gsocket/releases/latest \
  | python3 -c "import sys,json; print(next(a['browser_download_url'] for a in json.load(sys.stdin)['assets'] if a['name']=='gs-netcat_linux-x86_64'))" \
  | xargs wget -qO /usr/local/bin/gs-netcat
chmod +x /usr/local/bin/gs-netcat
```

### 3. Install MCP server dependencies

```bash
pip install -r containers/panel-mcp/requirements.txt
```

### 4. Configure CyberStrikeAI

In your `config.yaml`, find the `gs-panel` block under `external_mcp.servers`
and fill in the two required values:

```yaml
external_mcp:
  servers:
    gs-panel:
      transport: stdio
      command: python3
      args: ["containers/panel-mcp/mcp_server.py"]
      env:
        CSAI_URL: "http://localhost:8080"   # CyberStrikeAI base URL
        CSAI_PASSWORD: "your-operator-password"
      external_mcp_enable: true
```

Restart CyberStrikeAI. The `gs-panel` MCP server starts automatically.

---

## Deploying a container

### Via web UI (operator)

1. Open the **Containers** tab in CyberStrikeAI.
2. Click **New Container**, enter a name, optionally enter the panel URL for
   the registration callback, then click **Generate**.
3. Copy the `docker run` command and paste it on the target host.
4. The container starts, registers itself, and appears in the table as **Online**
   within seconds.

The gsocket secret is stored in CyberStrikeAI's database — you can retrieve the
deploy command again at any time via the **Deploy Command** button next to any
container in the table.

### Via AI agent (recommended for automation)

Ask CyberStrikeAI:

> "Deploy a new container and call it kali-op2"

The agent calls the `deploy_container` MCP tool, which creates the DB record and
returns the `docker run` command. Paste it on the target host.

### Manual (no UI)

```bash
python3 -c "import secrets; print(secrets.token_urlsafe(18)[:24])"
```

Register the container in the DB with a POST, or use the UI.

---

## MCP tools reference

### `list_containers`
Returns all registered containers with ID, name, hostname, IP, online status.

### `exec`
Run any shell command on a container via gsocket. stdout + stderr + returncode.

```
host     - container ID, name, or hostname substring
cmd      - shell command
timeout  - seconds (default 60, max 600)
```

### `scan`
Run nmap or rustscan from the container against a target.

```
host    - container
target  - IP, hostname, or CIDR
flags   - nmap flags (default: -sT -sV --open -T4)
fast    - true = rustscan first, then nmap
```

### `nuclei_scan`
Run nuclei from the container.

```
host      - container
target    - URL or IP
templates - comma-separated tags (default: cves,exposures)
severity  - critical,high,medium,low (optional)
```

### `upload_file` / `download_file`
Send or retrieve files via base64-encoded content.

### `deploy_container`
Create a new container record and return the `docker run` command.

```
name      - label (default: kali-cs)
panel_url - CyberStrikeAI URL for container callback (optional)
```

---

## Human operator shell access

From any host with gsocket installed:

```bash
gs-netcat -s <GS_SECRET>
```

This opens a fully interactive shell into the container over the gsocket relay.
The secret is shown in the **Deploy Command** modal in the CyberStrikeAI UI.

---

## Troubleshooting

**Container doesn't appear as Online after `docker run`**

Check container logs: `docker logs <name>`. The entrypoint prints
`[+] Registered with panel` or `[!] Panel registration failed`. If
`PANEL_URL` is set, verify it resolves from inside the container. The gsocket
listener starts regardless of registration status.

**`exec` times out**

gsocket relay adds latency. For long-running commands (e.g. full nmap scan),
pass `timeout: 300` or use the dedicated `scan` tool.

**`nuclei_scan` returns empty output**

Update templates inside the container:

```
exec host="..." cmd="nuclei -update-templates -silent"
```
