#!/bin/bash
set -e

: "${GS_SECRET:?GS_SECRET is required}"

# Self-register with CyberStrikeAI panel (updates hostname/IP, marks container online)
if [[ -n "$PANEL_URL" ]]; then
    HOSTNAME=$(hostname)
    IP=$(curl -sf --max-time 5 https://api.ipify.org 2>/dev/null || echo "unknown")
    curl -sf --max-time 10 \
        -X POST "$PANEL_URL/api/containers/register" \
        -H "Content-Type: application/json" \
        -d "{\"gs_secret\":\"$GS_SECRET\",\"hostname\":\"$HOSTNAME\",\"ip\":\"$IP\"}" \
        >/dev/null && echo "[+] Registered with panel at $PANEL_URL" || echo "[!] Panel registration failed (continuing)"
fi

echo "[+] Starting gsocket listener (secret: ${GS_SECRET:0:6}...)"
# -L = listen loop (accept multiple connections); -e /bin/sh = exec shell per connection
exec gs-netcat -L -s "$GS_SECRET" -e /bin/sh
