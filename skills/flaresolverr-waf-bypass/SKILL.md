# FlareSolverr WAF Bypass & Cookie Reuse Methodology

## Overview

FlareSolverr runs a real Chromium browser that solves Cloudflare, Akamai, and WAF challenge pages. Once solved, it exports clearance cookies and the accepted user-agent. These can be reused with ANY tool — curl, nuclei, ffuf, sqlmap, nikto, httpx — to access content that would otherwise return 403/challenge pages.

**This is the #1 technique for accessing WAF-protected targets during authorized pentesting.**

## When to Use FlareSolverr

- Target returns **403 Forbidden** or an HTML challenge page to curl/httpx
- **Nuclei/ffuf/nikto scans return empty** or only challenge responses
- Target is behind **Cloudflare, Akamai, Imperva, AWS WAF, Sucuri**
- You see `cf_clearance`, `__cf_bm`, `_cf_chl_*` cookies in browser DevTools
- HTTP response headers contain `cf-ray`, `server: cloudflare`
- You need to **maintain authenticated sessions** across multiple tools

## Quick Start

```bash
# Step 1: Solve the challenge and extract cookies
flaresolverr --url https://target.example --cookies-only

# Step 2: Use the extracted cookies with any tool
curl -H "Cookie: cf_clearance=abc123; __cf_bm=xyz" \
     -A "Mozilla/5.0 (X11; Linux x86_64)..." \
     https://target.example/admin

# Step 3: Feed cookies to scanning tools
nuclei -u https://target.example \
       -H "Cookie: cf_clearance=abc123" \
       -H "User-Agent: Mozilla/5.0..."
```

## Cookie Reuse Workflow (Step by Step)

### Phase 1: Detect WAF Protection

Before using FlareSolverr, confirm the target is WAF-protected:

```bash
# Quick check — if this returns 403 or challenge HTML, WAF is active
curl -sI https://target.example | head -20

# Look for these indicators:
# - HTTP 403 with "cf-ray" header → Cloudflare
# - HTTP 403 with "server: AkamaiGHost" → Akamai
# - JavaScript challenge page in body
# - "Checking your browser" text
# - "__cf_bm" or "cf_clearance" in Set-Cookie
```

### Phase 2: Extract Clearance Cookies

```bash
# Basic cookie extraction
flaresolverr --url https://target.example --cookies-only

# Output:
# {
#   "cookie_header": "cf_clearance=abc123; __cf_bm=xyz789",
#   "user_agent": "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36...",
#   "cookies": [
#     {"name": "cf_clearance", "value": "abc123", "domain": ".target.example", ...},
#     {"name": "__cf_bm", "value": "xyz789", "domain": ".target.example", ...}
#   ]
# }
```

**Save both `cookie_header` and `user_agent` — you need BOTH for the bypass to work.**

### Phase 3: Reuse Cookies with Security Tools

#### curl
```bash
curl -H "Cookie: <cookie_header>" \
     -H "User-Agent: <user_agent>" \
     https://target.example/api/v1/users
```

#### nuclei
```bash
nuclei -u https://target.example \
       -H "Cookie: <cookie_header>" \
       -H "User-Agent: <user_agent>" \
       -t cves/ -t vulnerabilities/
```

#### ffuf (directory bruteforce)
```bash
ffuf -u https://target.example/FUZZ \
     -w /usr/share/wordlists/common.txt \
     -H "Cookie: <cookie_header>" \
     -H "User-Agent: <user_agent>"
```

#### sqlmap
```bash
sqlmap -u "https://target.example/page?id=1" \
       --cookie="<cookie_header>" \
       --user-agent="<user_agent>" \
       --batch
```

#### nikto
```bash
nikto -h https://target.example \
      -C all \
      -useragent "<user_agent>" \
      -o nikto_results.txt
# Note: nikto doesn't directly support cookie injection via CLI;
# use FlareSolverr as a proxy or modify the request with a wrapper
```

#### httpx
```bash
echo "https://target.example" | httpx \
     -H "Cookie: <cookie_header>" \
     -H "User-Agent: <user_agent>" \
     -status-code -title -tech-detect
```

#### gobuster
```bash
gobuster dir -u https://target.example \
             -w /usr/share/wordlists/common.txt \
             -c "<cookie_header>" \
             -a "<user_agent>"
```

#### feroxbuster
```bash
feroxbuster -u https://target.example \
            -H "Cookie: <cookie_header>" \
            -H "User-Agent: <user_agent>" \
            -w /usr/share/wordlists/common.txt
```

### Phase 4: Session Persistence (Multi-Step Workflows)

For workflows that require maintaining browser state across multiple requests:

```bash
# Create a persistent session
flaresolverr --cmd sessions.create --session-id pentest-session-1

# Use the session for multiple requests (cookies are maintained)
flaresolverr --url https://target.example/login --session-id pentest-session-1
flaresolverr --url https://target.example/admin --session-id pentest-session-1
flaresolverr --url https://target.example/api/users --session-id pentest-session-1

# Clean up when done
flaresolverr --cmd sessions.destroy --session-id pentest-session-1
```

### Phase 5: Cookie Refresh

Cloudflare clearance cookies typically expire after **15-30 minutes**. Signs they've expired:
- Tools start getting 403 again
- Response body contains challenge HTML
- New `cf_chl_*` cookies appear

**Refresh:**
```bash
# Just re-run the cookie extraction
flaresolverr --url https://target.example --cookies-only
# Update the cookie_header in your subsequent tool commands
```

## Advanced Techniques

### POST Request Bypass (Login Forms)

```bash
flaresolverr --cmd request.post \
             --url https://target.example/login \
             --post-data "username=admin&password=test"
```

### Custom Headers

```bash
flaresolverr --url https://target.example \
             --headers-json '{"X-Forwarded-For":"127.0.0.1","Accept-Language":"en-US"}'
```

### Through Proxy (Tor/SOCKS5)

```bash
flaresolverr --url https://target.example \
             --proxy-url socks5://127.0.0.1:9050 \
             --cookies-only
```

### Full Page Content + Cookies

```bash
# Get both the rendered page content AND cookies (default mode)
flaresolverr --url https://target.example

# Output includes:
# - solution.response (full rendered HTML after JS execution)
# - solution.cookies (all cookies)
# - solution.userAgent
# - solution.status (HTTP status code)
```

## Cookie Types and Their Purposes

| Cookie | Set By | Purpose | Lifetime |
|--------|--------|---------|----------|
| `cf_clearance` | Cloudflare | Challenge solved proof | 15-30 min |
| `__cf_bm` | Cloudflare | Bot management | ~30 min |
| `_cf_chl_*` | Cloudflare | Challenge tracking | Session |
| `__cflb` | Cloudflare | Load balancer affinity | Session |
| `akaalb_*` | Akamai | Load balancer | Session |
| `_abck` | Akamai | Bot detection | ~1 hour |
| `bm_sz` | Akamai | Bot manager size | Session |

## Troubleshooting

### FlareSolverr not responding
```bash
# Check if service is running
curl -s http://127.0.0.1:8191/ | head -5

# Docker: restart the container
docker restart flaresolverr

# Standalone: restart the service
systemctl restart flaresolverr
```

### Challenge not solved (timeout)
```bash
# Increase timeout (default 60s)
flaresolverr --url https://target.example --max-timeout 120000

# Some challenges require multiple attempts
flaresolverr --url https://target.example --cookies-only  # try again
```

### Cookies not working with other tools
- Ensure you're using BOTH `cookie_header` AND `user_agent` — WAFs check both
- Check cookie domain matches your request domain
- Cookies may have expired — re-extract them
- Some WAFs also check TLS fingerprint (JA3) — FlareSolverr's Chromium fingerprint may differ from curl's

## Integration with CyberStrikeAI Agent

The AI agent should follow this pattern automatically:
1. First attempt with direct tool (nuclei, ffuf, etc.)
2. If 403/challenge detected → call `flaresolverr --cookies-only`
3. Extract `cookie_header` and `user_agent` from the response
4. Re-run the original tool with cookie/UA headers injected
5. Set a timer to refresh cookies every 15 minutes during long scans

This is the standard WAF bypass workflow for all authorized penetration testing.
