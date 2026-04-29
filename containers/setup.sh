#!/usr/bin/env bash
# CyberStrikeAI container fleet setup
# Downloads gs-netcat binary; optionally builds the Kali image.
#
# Usage:
#   ./containers/setup.sh           # download gs-netcat if missing, print config
#   ./containers/setup.sh --build   # also build cyberstrike/kali:latest
#   ./containers/setup.sh --check   # silent mode used by run.sh
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_DIR="$ROOT_DIR/containers/bin"
GS_BIN="$BIN_DIR/gs-netcat"
KALI_DIR="$ROOT_DIR/containers/kali"

BUILD_IMAGE=0
SILENT=0

for arg in "$@"; do
  case "$arg" in
    --build)  BUILD_IMAGE=1 ;;
    --check)  SILENT=1 ;;
  esac
done

log()  { [[ "$SILENT" -eq 0 ]] && echo -e "\033[34m[containers]\033[0m $*" || true; }
ok()   { [[ "$SILENT" -eq 0 ]] && echo -e "\033[32m[containers]\033[0m $*" || true; }
warn() { echo -e "\033[33m[containers]\033[0m $*" >&2; }

have() { command -v "$1" >/dev/null 2>&1; }

# ── detect architecture ────────────────────────────────────────────────────────
detect_gs_asset() {
  local arch
  arch="$(uname -m)"
  case "$arch" in
    x86_64)  echo "gs-netcat_linux-x86_64" ;;
    aarch64) echo "gs-netcat_linux-aarch64" ;;
    armv7l)  echo "gs-netcat_linux-armhf" ;;
    *)       warn "Unsupported architecture: $arch — install gs-netcat manually"; return 1 ;;
  esac
}

# ── download gs-netcat if missing ─────────────────────────────────────────────
install_gs_netcat() {
  if [[ -x "$GS_BIN" ]]; then
    ok "gs-netcat already at $GS_BIN"
    return 0
  fi

  local asset
  asset="$(detect_gs_asset)"

  log "Downloading gs-netcat ($asset) from GitHub..."
  mkdir -p "$BIN_DIR"

  local api_url="https://api.github.com/repos/hackerschoice/gsocket/releases/latest"
  local download_url

  if have curl; then
    download_url="$(curl -sf "$api_url" \
      | python3 -c "import sys,json; print(next(a['browser_download_url'] for a in json.load(sys.stdin)['assets'] if a['name']=='$asset'))")"
    curl -sfL "$download_url" -o "$GS_BIN"
  elif have wget; then
    download_url="$(wget -qO- "$api_url" \
      | python3 -c "import sys,json; print(next(a['browser_download_url'] for a in json.load(sys.stdin)['assets'] if a['name']=='$asset'))")"
    wget -qO "$GS_BIN" "$download_url"
  else
    warn "Neither curl nor wget found — cannot download gs-netcat"
    warn "Install manually: https://github.com/hackerschoice/gsocket/releases"
    return 1
  fi

  chmod +x "$GS_BIN"
  ok "gs-netcat installed: $GS_BIN"
}

# ── verify Python MCP deps ────────────────────────────────────────────────────
check_python_deps() {
  # mcp and httpx are in requirements.txt and installed into venv by run.sh.
  # This is just a sanity check in non-venv contexts (e.g. Docker).
  if python3 -c "import mcp, httpx" 2>/dev/null; then
    ok "Python deps OK (mcp, httpx)"
  else
    warn "Python deps 'mcp' and/or 'httpx' not found. Run: pip install mcp httpx"
  fi
}

# ── build Kali image ──────────────────────────────────────────────────────────
build_kali_image() {
  if ! have docker; then
    warn "Docker not found — skipping Kali image build"
    return 0
  fi
  log "Building cyberstrike/kali:latest (this takes 10-20 min on first run)..."
  docker build -t cyberstrike/kali:latest "$KALI_DIR"
  ok "Kali image built: cyberstrike/kali:latest"
}

# ── print post-setup instructions ─────────────────────────────────────────────
print_summary() {
  [[ "$SILENT" -eq 1 ]] && return 0
  echo ""
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  echo "  Container fleet setup complete"
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  echo ""
  echo "1. In config.yaml, enable gs-panel under external_mcp.servers:"
  echo ""
  echo "   gs-panel:"
  echo "     transport: stdio"
  echo "     command: python3"
  echo "     args: [\"containers/panel-mcp/mcp_server.py\"]"
  echo "     env:"
  echo "       CSAI_URL: \"http://localhost:8080\""
  echo "       CSAI_PASSWORD: \"your-operator-password\""
  echo "       GS_NETCAT_BIN: \"${GS_BIN}\""
  echo "     external_mcp_enable: true"
  echo ""
  echo "2. Deploy a container from the Containers tab in the web UI,"
  echo "   or ask the AI: \"deploy a new container called kali-op1\""
  echo ""
  if [[ "$BUILD_IMAGE" -eq 0 ]]; then
    echo "3. To pre-build the Kali Docker image on this host:"
    echo "   ./containers/setup.sh --build"
    echo ""
  fi
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
}

install_gs_netcat
check_python_deps

if [[ "$BUILD_IMAGE" -eq 1 ]]; then
  build_kali_image
fi

print_summary
