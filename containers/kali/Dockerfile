FROM kalilinux/kali-rolling

ENV DEBIAN_FRONTEND=noninteractive \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

# ── apt: everything available in kali-rolling ────────────────────────────────
RUN apt-get update && apt-get install -y --no-install-recommends \
    # Core
    python3 python3-pip python3-venv curl wget git jq ca-certificates unzip \
    dnsutils iputils-ping net-tools iproute2 \
    # Port scanning
    nmap masscan zmap \
    # Web / fuzzing
    sqlmap ffuf gobuster nikto \
    # Brute force
    hydra \
    # Subdomain / DNS
    sublist3r subfinder amass dnsrecon fierce \
    # HTTP toolkit
    httpx-toolkit \
    # Vulnerability scanning
    nuclei \
    # SMB / Windows / AD
    netexec impacket-scripts smbmap enum4linux-ng smbclient \
    # Misc
    wordlists responder \
    && apt-get clean && rm -rf /var/lib/apt/lists/*

# ── Go tools: pre-built binaries from GitHub releases ────────────────────────
# dalfox (binary inside tar is named dalfox-linux-amd64)
RUN set -e; \
    VER=$(curl -sf https://api.github.com/repos/hahwul/dalfox/releases/latest | python3 -c "import sys,json; print(json.load(sys.stdin)['tag_name'][1:])"); \
    wget -qO /tmp/dalfox.tar.gz "https://github.com/hahwul/dalfox/releases/download/v${VER}/dalfox-linux-amd64.tar.gz"; \
    tar -xzf /tmp/dalfox.tar.gz -C /usr/local/bin/ dalfox-linux-amd64; \
    mv /usr/local/bin/dalfox-linux-amd64 /usr/local/bin/dalfox; \
    chmod +x /usr/local/bin/dalfox; rm /tmp/dalfox.tar.gz

# katana
RUN set -e; \
    VER=$(curl -sf https://api.github.com/repos/projectdiscovery/katana/releases/latest | python3 -c "import sys,json; print(json.load(sys.stdin)['tag_name'][1:])"); \
    wget -qO /tmp/katana.zip "https://github.com/projectdiscovery/katana/releases/download/v${VER}/katana_${VER}_linux_amd64.zip"; \
    unzip -qo /tmp/katana.zip katana -d /usr/local/bin/; \
    chmod +x /usr/local/bin/katana; rm /tmp/katana.zip

# waybackurls
RUN set -e; \
    VER=$(curl -sf https://api.github.com/repos/tomnomnom/waybackurls/releases/latest | python3 -c "import sys,json; print(json.load(sys.stdin)['tag_name'][1:])"); \
    wget -qO /tmp/waybackurls.tgz "https://github.com/tomnomnom/waybackurls/releases/download/v${VER}/waybackurls-linux-amd64-${VER}.tgz"; \
    tar -xzf /tmp/waybackurls.tgz -C /usr/local/bin/; \
    chmod +x /usr/local/bin/waybackurls; rm /tmp/waybackurls.tgz

# gau
RUN set -e; \
    VER=$(curl -sf https://api.github.com/repos/lc/gau/releases/latest | python3 -c "import sys,json; print(json.load(sys.stdin)['tag_name'][1:])"); \
    wget -qO /tmp/gau.tar.gz "https://github.com/lc/gau/releases/download/v${VER}/gau_${VER}_linux_amd64.tar.gz"; \
    tar -xzf /tmp/gau.tar.gz -C /usr/local/bin/ gau; \
    chmod +x /usr/local/bin/gau; rm /tmp/gau.tar.gz

# ── rustscan (find latest release that actually has a .deb asset) ─────────────
RUN set -e; \
    URL=$(curl -sfL "https://api.github.com/repositories/278933035/releases?per_page=10" \
          | python3 -c "import sys,json; rs=json.load(sys.stdin); print(next(a['browser_download_url'] for r in rs for a in r['assets'] if a['name'].endswith('_amd64.deb')))"); \
    wget -qO /tmp/rustscan.deb "$URL"; \
    dpkg -i /tmp/rustscan.deb; rm /tmp/rustscan.deb

# ── gsocket (static binary, no deps) ─────────────────────────────────────────
RUN set -e; \
    URL=$(curl -sf https://api.github.com/repos/hackerschoice/gsocket/releases/latest \
          | python3 -c "import sys,json; print(next(a['browser_download_url'] for a in json.load(sys.stdin)['assets'] if a['name'] == 'gs-netcat_linux-x86_64'))"); \
    wget -qO /usr/local/bin/gs-netcat "$URL"; \
    chmod +x /usr/local/bin/gs-netcat; \
    ln -sf /usr/local/bin/gs-netcat /usr/local/bin/gsocket

# ── crlfuzz (Go binary, not on PyPI) ─────────────────────────────────────────
RUN set -e; \
    VER=$(curl -sf https://api.github.com/repos/dwisiswant0/crlfuzz/releases/latest | python3 -c "import sys,json; print(json.load(sys.stdin)['tag_name'][1:])"); \
    wget -qO /tmp/crlfuzz.tar.gz "https://github.com/dwisiswant0/crlfuzz/releases/download/v${VER}/crlfuzz_${VER}_linux_amd64.tar.gz"; \
    tar -xzf /tmp/crlfuzz.tar.gz -C /usr/local/bin/ crlfuzz; \
    chmod +x /usr/local/bin/crlfuzz; rm /tmp/crlfuzz.tar.gz

# ── Python tools ──────────────────────────────────────────────────────────────
RUN pip3 install --break-system-packages --no-cache-dir requests httpx rich \
    && pip3 install --break-system-packages --no-cache-dir \
       git+https://github.com/devanshbatham/paramspider.git

# ── Playwright (AI browser control) ──────────────────────────────────────────
RUN pip3 install --break-system-packages --no-cache-dir playwright \
    && playwright install chromium --with-deps

# ── MCP server deps ───────────────────────────────────────────────────────────
RUN pip3 install --break-system-packages --no-cache-dir mcp

# ── nuclei templates ──────────────────────────────────────────────────────────
RUN nuclei -update-templates -silent 2>/dev/null || true

WORKDIR /workspace

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
