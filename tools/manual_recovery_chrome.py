from __future__ import annotations

import argparse
import json
import os
import socket
import subprocess
import time
from pathlib import Path
from urllib.parse import parse_qs, urlparse

import requests


def default_chrome_path() -> str:
    candidates = [
        os.path.join(os.environ.get("ProgramFiles", ""), "Google", "Chrome", "Application", "chrome.exe"),
        os.path.join(os.environ.get("ProgramFiles(x86)", ""), "Google", "Chrome", "Application", "chrome.exe"),
        os.path.join(os.environ.get("LOCALAPPDATA", ""), "Google", "Chrome", "Application", "chrome.exe"),
    ]
    for candidate in candidates:
        if candidate and os.path.exists(candidate):
            return candidate
    return "chrome.exe"


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def chrome_proxy_arg(proxy: str) -> str:
    proxy = str(proxy or "").strip()
    if not proxy:
        return ""
    parsed = urlparse(proxy)
    if not parsed.scheme or not parsed.hostname:
        return proxy
    host = parsed.hostname
    if parsed.port:
        host = f"{host}:{parsed.port}"
    return f"{parsed.scheme}://{host}"


def request_proxies(proxy: str) -> dict | None:
    proxy = str(proxy or "").strip()
    if not proxy:
        return None
    return {"http": proxy, "https": proxy}


def code_from_url(value: str) -> tuple[str, str]:
    parsed = urlparse(value)
    query = parse_qs(parsed.query)
    code = (query.get("code") or [""])[0]
    error = (query.get("error_description") or query.get("error") or [""])[0]
    return code, error


def exchange_code(args, code: str) -> dict:
    resp = requests.post(
        args.token_url,
        data={
            "client_id": args.client_id,
            "scope": args.scope,
            "code": code,
            "redirect_uri": args.redirect_uri,
            "grant_type": "authorization_code",
        },
        headers={"Content-Type": "application/x-www-form-urlencoded"},
        proxies=request_proxies(args.proxy),
        timeout=60,
    )
    payload = resp.json()
    if resp.status_code < 200 or resp.status_code >= 300:
        raise RuntimeError(payload.get("error_description") or payload.get("error") or resp.text)
    if not payload.get("refresh_token"):
        raise RuntimeError("token exchange returned empty refresh_token")
    return payload


def complete_oauth(args, tokens: dict) -> None:
    resp = requests.post(
        args.complete_url,
        json={
            "email_address": args.email,
            "refresh_token": tokens.get("refresh_token", ""),
            "access_token": tokens.get("access_token", ""),
        },
        timeout=30,
    )
    if resp.status_code < 200 or resp.status_code >= 300:
        raise RuntimeError(resp.text)


def browser_tabs(debug_port: int) -> list[dict]:
    resp = requests.get(f"http://127.0.0.1:{debug_port}/json/list", timeout=2)
    if resp.status_code < 200 or resp.status_code >= 300:
        return []
    return resp.json()


def main() -> int:
    parser = argparse.ArgumentParser(description="Open Chrome Incognito for mailbox recovery and local Outlook OAuth")
    parser.add_argument("--proxy", default="")
    parser.add_argument("--profile-dir", required=True)
    parser.add_argument("--url", default="https://login.live.com/")
    parser.add_argument("--hold-seconds", type=int, default=7200)
    parser.add_argument("--email", required=True)
    parser.add_argument("--authorize-url", required=True)
    parser.add_argument("--client-id", required=True)
    parser.add_argument("--redirect-uri", required=True)
    parser.add_argument("--scope", required=True)
    parser.add_argument("--token-url", default="https://login.microsoftonline.com/common/oauth2/v2.0/token")
    parser.add_argument("--complete-url", required=True)
    parser.add_argument("--chrome-path", default=default_chrome_path())
    args = parser.parse_args()

    profile_dir = Path(args.profile_dir)
    profile_dir.mkdir(parents=True, exist_ok=True)
    debug_port = free_port()
    proxy_arg = chrome_proxy_arg(args.proxy)
    chrome_args = [
        args.chrome_path,
        f"--remote-debugging-port={debug_port}",
        "--remote-debugging-address=127.0.0.1",
        f"--user-data-dir={str(profile_dir)}",
        "--incognito",
        "--no-first-run",
        "--no-default-browser-check",
        "--new-window",
    ]
    if proxy_arg:
        chrome_args.append(f"--proxy-server={proxy_arg}")
    chrome_args.append(args.authorize_url or args.url)

    proc = subprocess.Popen(chrome_args)
    deadline = time.time() + max(60, int(args.hold_seconds or 7200))
    try:
        while time.time() < deadline:
            for tab in browser_tabs(debug_port):
                current_url = str(tab.get("url") or "")
                code, error = code_from_url(current_url)
                if error:
                    raise RuntimeError(error)
                if code:
                    tokens = exchange_code(args, code)
                    complete_oauth(args, tokens)
                    print(json.dumps({"ok": True, "email_address": args.email}, ensure_ascii=False))
                    time.sleep(3)
                    return 0
            if proc.poll() is not None:
                return proc.returncode or 0
            time.sleep(1)
        return 0
    finally:
        if proc.poll() is None:
            proc.terminate()


if __name__ == "__main__":
    raise SystemExit(main())
