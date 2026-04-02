#!/usr/bin/env python3
"""
Shodan Internet Search Engine Plugin for CyberStrikeAI
======================================================
Host discovery, service fingerprinting, vulnerability detection,
DNS resolution, exploit search, and network intelligence.

Auth: query parameter key=API_KEY on every request.
Base URL: https://api.shodan.io (main), https://exploits.shodan.io/api (exploits)

Supports both free and paid API tiers:
  Free:  host lookup, dns resolve/reverse, count, api-info, filters, facets, ports
  Paid:  full search (1 credit), exploits search, dns/domain (1 credit), on-demand scan
"""

import os
import sys
import json
import time

# ── Config ──────────────────────────────────────────────────────────
API_BASE = "https://api.shodan.io"
EXPLOITS_BASE = "https://exploits.shodan.io/api"
API_KEY = os.environ.get("SHODAN_API_KEY", "").strip()
TIMEOUT = 30
MAX_RETRIES = 3
RETRY_BACKOFF = 2  # seconds, multiplied by attempt number


def _get(url, params=None, timeout=TIMEOUT):
    """HTTP GET with auth, retry on 429, structured error handling."""
    import requests

    if params is None:
        params = {}
    params["key"] = API_KEY

    last_err = None
    for attempt in range(1, MAX_RETRIES + 1):
        try:
            resp = requests.get(url, params=params, timeout=timeout,
                                headers={"User-Agent": "CyberStrikeAI/1.0"})

            if resp.status_code == 200:
                return resp.json()

            if resp.status_code == 401:
                return {"error": "Invalid API key (401). Check your SHODAN_API_KEY in Settings > Plugins > Shodan.",
                        "status": "error", "http_code": 401}

            if resp.status_code == 402:
                return {"error": "Insufficient credits (402). This endpoint requires a paid Shodan plan or query credits.",
                        "status": "error", "http_code": 402}

            if resp.status_code == 404:
                try:
                    data = resp.json()
                    return {"error": data.get("error", "Not found"), "status": "error", "http_code": 404}
                except Exception:
                    return {"error": "Resource not found (404)", "status": "error", "http_code": 404}

            if resp.status_code == 429:
                wait = RETRY_BACKOFF * attempt
                last_err = f"Rate limited (429). Retrying in {wait}s (attempt {attempt}/{MAX_RETRIES})"
                time.sleep(wait)
                continue

            # Other errors
            try:
                data = resp.json()
                msg = data.get("error", resp.text[:300])
            except Exception:
                msg = resp.text[:300]
            return {"error": msg, "status": "error", "http_code": resp.status_code}

        except Exception as e:
            last_err = f"{type(e).__name__}: {str(e)}"
            if attempt < MAX_RETRIES:
                time.sleep(RETRY_BACKOFF * attempt)
                continue
            break

    return {"error": f"Request failed after {MAX_RETRIES} attempts: {last_err}", "status": "error"}


def mask_key(s, keep=6):
    """Mask API key for safe display."""
    if not s or len(s) <= keep * 2:
        return "*" * max(len(s), 8)
    return s[:keep] + "*" * (len(s) - keep * 2) + s[-4:]


# ── Commands ────────────────────────────────────────────────────────

def cmd_validate():
    """Validate API key and show plan info, credits, usage."""
    data = _get(f"{API_BASE}/api-info")
    if "error" in data and data.get("status") == "error":
        return data

    return {
        "status": "success",
        "command": "validate",
        "key_preview": mask_key(API_KEY),
        "plan": data.get("plan", "unknown"),
        "query_credits": data.get("query_credits", 0),
        "scan_credits": data.get("scan_credits", 0),
        "monitored_ips": data.get("monitored_ips"),
        "usage_limits": data.get("usage_limits", {}),
        "unlocked": data.get("unlocked", False),
        "unlocked_left": data.get("unlocked_left", 0),
        "https": data.get("https", False),
        "telnet": data.get("telnet", False),
    }


def cmd_host(ip, history=False, minify=False):
    """Look up all services on a specific IP address (free)."""
    params = {}
    if history:
        params["history"] = "true"
    if minify:
        params["minify"] = "true"

    data = _get(f"{API_BASE}/shodan/host/{ip}", params)
    if "error" in data and data.get("status") == "error":
        return data

    # Structure the output for readability
    result = {
        "status": "success",
        "command": "host",
        "ip": data.get("ip_str", ip),
        "hostnames": data.get("hostnames", []),
        "domains": data.get("domains", []),
        "country": data.get("country_name", ""),
        "country_code": data.get("country_code", ""),
        "city": data.get("city", ""),
        "org": data.get("org", ""),
        "isp": data.get("isp", ""),
        "asn": data.get("asn", ""),
        "os": data.get("os", ""),
        "ports": data.get("ports", []),
        "vulns": data.get("vulns", []),
        "tags": data.get("tags", []),
        "last_update": data.get("last_update", ""),
    }

    # Extract service details
    services = []
    for svc in data.get("data", []):
        entry = {
            "port": svc.get("port"),
            "transport": svc.get("transport", "tcp"),
            "product": svc.get("product", ""),
            "version": svc.get("version", ""),
            "module": svc.get("_shodan", {}).get("module", ""),
            "banner": (svc.get("data", "") or "")[:500],
        }
        # HTTP info
        if "http" in svc:
            http = svc["http"]
            entry["http"] = {
                "title": http.get("title", ""),
                "server": http.get("server", ""),
                "status": http.get("status"),
                "redirects": http.get("redirects", []),
            }
        # SSL/TLS info
        if "ssl" in svc:
            ssl = svc["ssl"]
            cert = ssl.get("cert", {})
            entry["ssl"] = {
                "cipher": ssl.get("cipher", {}),
                "subject_cn": cert.get("subject", {}).get("CN", ""),
                "issuer_cn": cert.get("issuer", {}).get("CN", ""),
                "expires": cert.get("expires", ""),
                "versions": ssl.get("versions", []),
                "jarm": ssl.get("jarm", ""),
            }
        # Vulns
        if "vulns" in svc:
            entry["vulns"] = list(svc["vulns"].keys()) if isinstance(svc["vulns"], dict) else svc["vulns"]
        services.append(entry)

    result["services"] = services
    result["total_services"] = len(services)
    return result


def cmd_search(query, page=1, limit=100, minify=True, facets=None):
    """Search Shodan (costs 1 query credit for filtered/paged queries)."""
    params = {
        "query": query,
        "page": page,
        "minify": "true" if minify else "false",
    }
    if facets:
        params["facets"] = facets

    data = _get(f"{API_BASE}/shodan/host/search", params)
    if "error" in data and data.get("status") == "error":
        return data

    matches = data.get("matches", [])
    results = []
    for m in matches[:limit]:
        entry = {
            "ip": m.get("ip_str", ""),
            "port": m.get("port"),
            "transport": m.get("transport", "tcp"),
            "hostnames": m.get("hostnames", []),
            "domains": m.get("domains", []),
            "org": m.get("org", ""),
            "isp": m.get("isp", ""),
            "asn": m.get("asn", ""),
            "os": m.get("os", ""),
            "country": m.get("location", {}).get("country_name", ""),
            "city": m.get("location", {}).get("city", ""),
            "product": m.get("product", ""),
            "version": m.get("version", ""),
            "banner": (m.get("data", "") or "")[:300],
        }
        if "http" in m:
            entry["http_title"] = m["http"].get("title", "")
            entry["http_server"] = m["http"].get("server", "")
        if "ssl" in m:
            cert = m["ssl"].get("cert", {})
            entry["ssl_cn"] = cert.get("subject", {}).get("CN", "")
        if "vulns" in m:
            entry["vulns"] = list(m["vulns"].keys()) if isinstance(m["vulns"], dict) else m["vulns"]
        results.append(entry)

    result = {
        "status": "success",
        "command": "search",
        "query": query,
        "total": data.get("total", 0),
        "page": page,
        "count": len(results),
        "results": results,
    }
    if data.get("facets"):
        result["facets"] = data["facets"]
    return result


def cmd_count(query, facets=None):
    """Count results without consuming credits."""
    params = {"query": query}
    if facets:
        params["facets"] = facets

    data = _get(f"{API_BASE}/shodan/host/count", params)
    if "error" in data and data.get("status") == "error":
        return data

    result = {
        "status": "success",
        "command": "count",
        "query": query,
        "total": data.get("total", 0),
    }
    if data.get("facets"):
        result["facets"] = data["facets"]
    return result


def cmd_dns_resolve(hostnames):
    """Resolve hostnames to IPs (free). Comma-separated list."""
    data = _get(f"{API_BASE}/dns/resolve", {"hostnames": hostnames})
    if "error" in data and data.get("status") == "error":
        return data

    entries = []
    for hostname, ip in data.items():
        if hostname == "key":
            continue
        entries.append({"hostname": hostname, "ip": ip})

    return {
        "status": "success",
        "command": "dns",
        "resolved": entries,
        "total": len(entries),
    }


def cmd_dns_reverse(ips):
    """Reverse DNS lookup (free). Comma-separated list of IPs."""
    data = _get(f"{API_BASE}/dns/reverse", {"ips": ips})
    if "error" in data and data.get("status") == "error":
        return data

    entries = []
    for ip, hostnames in data.items():
        if ip == "key":
            continue
        entries.append({"ip": ip, "hostnames": hostnames if hostnames else []})

    return {
        "status": "success",
        "command": "reverse",
        "resolved": entries,
        "total": len(entries),
    }


def cmd_dns_domain(domain, history=False, dns_type=None, page=1):
    """Get subdomains and DNS entries for a domain (1 query credit)."""
    params = {"page": page}
    if history:
        params["history"] = "true"
    if dns_type:
        params["type"] = dns_type

    data = _get(f"{API_BASE}/dns/domain/{domain}", params)
    if "error" in data and data.get("status") == "error":
        return data

    return {
        "status": "success",
        "command": "domain",
        "domain": domain,
        "subdomains": data.get("subdomains", []),
        "records": data.get("data", []),
        "total_subdomains": len(data.get("subdomains", [])),
        "tags": data.get("tags", []),
        "more": data.get("more", False),
    }


def cmd_exploits(query, page=1, facets=None):
    """Search Shodan exploits database."""
    params = {"query": query, "page": page}
    if facets:
        params["facets"] = facets

    data = _get(f"{EXPLOITS_BASE}/search", params)
    if "error" in data and data.get("status") == "error":
        return data

    matches = data.get("matches", [])
    results = []
    for m in matches:
        results.append({
            "id": m.get("_id", ""),
            "description": (m.get("description", "") or "")[:500],
            "author": m.get("author", ""),
            "code": (m.get("code", "") or "")[:1000],
            "source": m.get("source", ""),
            "date": m.get("date", ""),
            "cve": m.get("cve", []),
            "platform": m.get("platform", ""),
            "type": m.get("type", ""),
            "port": m.get("port"),
        })

    result = {
        "status": "success",
        "command": "exploits",
        "query": query,
        "total": data.get("total", 0),
        "count": len(results),
        "results": results,
    }
    if data.get("facets"):
        result["facets"] = data["facets"]
    return result


def cmd_myip():
    """Get your current public IP address (free)."""
    data = _get(f"{API_BASE}/tools/myip")
    if isinstance(data, str):
        return {"status": "success", "command": "myip", "ip": data}
    if "error" in data and data.get("status") == "error":
        return data
    return {"status": "success", "command": "myip", "ip": str(data)}


def cmd_ports():
    """List all ports Shodan crawls (free)."""
    data = _get(f"{API_BASE}/shodan/ports")
    if isinstance(data, list):
        return {"status": "success", "command": "ports", "total": len(data), "ports": data}
    if "error" in data and data.get("status") == "error":
        return data
    return {"status": "success", "command": "ports", "data": data}


def cmd_filters():
    """List all available Shodan search filters (free)."""
    data = _get(f"{API_BASE}/shodan/host/search/filters")
    if isinstance(data, list):
        return {"status": "success", "command": "filters", "total": len(data), "filters": data}
    if "error" in data and data.get("status") == "error":
        return data
    return {"status": "success", "command": "filters", "data": data}


def cmd_facets():
    """List all available Shodan search facets (free)."""
    data = _get(f"{API_BASE}/shodan/host/search/facets")
    if isinstance(data, list):
        return {"status": "success", "command": "facets", "total": len(data), "facets": data}
    if "error" in data and data.get("status") == "error":
        return data
    return {"status": "success", "command": "facets", "data": data}


# ── Argument Parsing ────────────────────────────────────────────────

def parse_args():
    """Parse arguments: supports JSON object or positional args."""
    if len(sys.argv) > 1:
        # Try JSON mode first (from tool framework)
        try:
            config = json.loads(sys.argv[1])
            if isinstance(config, dict):
                return config
        except (json.JSONDecodeError, TypeError):
            pass

    # Positional mode: query [command] [limit] [page] [facets]
    config = {}
    args = sys.argv[1:]

    # Parse flags
    positionals = []
    i = 0
    while i < len(args):
        arg = args[i]
        if arg == "--history":
            config["history"] = True
        elif arg == "--minify":
            config["minify"] = True
        elif arg == "--facets" and i + 1 < len(args):
            config["facets"] = args[i + 1]
            i += 1
        elif arg == "--page" and i + 1 < len(args):
            config["page"] = int(args[i + 1])
            i += 1
        elif arg == "--limit" and i + 1 < len(args):
            config["limit"] = int(args[i + 1])
            i += 1
        elif arg == "--type" and i + 1 < len(args):
            config["dns_type"] = args[i + 1]
            i += 1
        else:
            positionals.append(arg)
        i += 1

    if len(positionals) > 0:
        config["query"] = positionals[0]
    if len(positionals) > 1:
        config["command"] = positionals[1]
    if len(positionals) > 2:
        try:
            config["limit"] = int(positionals[2])
        except ValueError:
            pass

    return config


# ── Main ────────────────────────────────────────────────────────────

def main():
    if not API_KEY:
        print(json.dumps({
            "status": "error",
            "message": "SHODAN_API_KEY not configured. Set your API key in Settings > Plugins > Shodan.",
            "note": "Get your API key at https://account.shodan.io",
            "free_tier": "Free tier supports: host lookup, DNS resolve/reverse, count, api-info",
            "paid_tier": "Paid tier adds: full search, exploits, domain DNS, on-demand scanning",
        }, indent=2))
        sys.exit(1)

    config = parse_args()
    query = config.get("query", "").strip()
    command = config.get("command", "").strip().lower()

    # Auto-detect command from query if not specified
    if not command:
        if query in ("validate", "credits", "info", "status", "api-info"):
            command = "validate"
        elif query in ("myip", "my-ip", "ip"):
            command = "myip"
        elif query in ("ports", "port-list"):
            command = "ports"
        elif query in ("filters", "filter-list"):
            command = "filters"
        elif query in ("facets", "facet-list"):
            command = "facets"
        else:
            command = "search"  # default

    # Also support command=validate with empty query
    if command in ("validate", "credits", "info", "status"):
        result = cmd_validate()
    elif command == "myip":
        result = cmd_myip()
    elif command == "ports":
        result = cmd_ports()
    elif command == "filters":
        result = cmd_filters()
    elif command == "facets":
        result = cmd_facets()
    elif not query:
        print(json.dumps({
            "status": "error",
            "message": "Missing required parameter: query",
            "commands": {
                "search": "Search Shodan (1 credit for filtered/paged queries)",
                "host": "Look up all services on a specific IP (free)",
                "count": "Count results only, no credits consumed (free)",
                "dns": "Resolve hostnames to IPs (free)",
                "reverse": "Reverse DNS lookup (free)",
                "domain": "Get subdomains and DNS entries (1 credit)",
                "exploits": "Search exploits database",
                "validate": "Check API key and credits (free)",
                "myip": "Show your public IP (free)",
                "ports": "List all ports Shodan crawls (free)",
                "filters": "List available search filters (free)",
                "facets": "List available search facets (free)",
            },
            "query_examples": [
                "apache port:443",
                'org:"Google" country:US',
                "net:1.2.3.0/24",
                'vuln:CVE-2021-44228',
                'http.title:"Dashboard"',
                'ssl.cert.subject.cn:"example.com"',
                "port:22 -country:CN",
            ],
        }, indent=2))
        sys.exit(1)
    elif command == "host":
        result = cmd_host(query,
                          history=config.get("history", False),
                          minify=config.get("minify", False))
    elif command == "search":
        result = cmd_search(query,
                            page=config.get("page", 1),
                            limit=config.get("limit", 100),
                            facets=config.get("facets"))
    elif command == "count":
        result = cmd_count(query, facets=config.get("facets"))
    elif command in ("dns", "resolve"):
        result = cmd_dns_resolve(query)
    elif command in ("reverse", "rdns", "reverse-dns"):
        result = cmd_dns_reverse(query)
    elif command == "domain":
        result = cmd_dns_domain(query,
                                history=config.get("history", False),
                                dns_type=config.get("dns_type"),
                                page=config.get("page", 1))
    elif command in ("exploits", "exploit"):
        result = cmd_exploits(query,
                              page=config.get("page", 1),
                              facets=config.get("facets"))
    else:
        # Unknown command, try as search
        result = cmd_search(query,
                            page=config.get("page", 1),
                            limit=config.get("limit", 100),
                            facets=config.get("facets"))

    try:
        print(json.dumps(result, indent=2, default=str))
        sys.exit(0 if result.get("status") == "success" else 1)
    except Exception as e:
        print(json.dumps({"status": "error", "message": f"{type(e).__name__}: {str(e)}"}))
        sys.exit(1)


if __name__ == "__main__":
    main()
