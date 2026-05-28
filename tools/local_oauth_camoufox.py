from __future__ import annotations

import argparse
import json
import time
from pathlib import Path
from urllib.parse import parse_qs, urlparse

import requests


def proxy_config(proxy: str) -> dict | None:
    proxy = str(proxy or "").strip()
    if not proxy:
        return None
    parsed = urlparse(proxy)
    if not parsed.scheme or not parsed.hostname:
        return None
    server = f"{parsed.scheme}://{parsed.hostname}:{parsed.port}" if parsed.port else f"{parsed.scheme}://{parsed.hostname}"
    return {
        "server": server,
        "username": parsed.username or "",
        "password": parsed.password or "",
    }


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


def main() -> int:
    parser = argparse.ArgumentParser(description="Run Outlook OAuth in the existing local Camoufox recovery profile")
    parser.add_argument("--email", required=True)
    parser.add_argument("--proxy", default="")
    parser.add_argument("--profile-dir", required=True)
    parser.add_argument("--authorize-url", required=True)
    parser.add_argument("--client-id", required=True)
    parser.add_argument("--redirect-uri", required=True)
    parser.add_argument("--scope", required=True)
    parser.add_argument("--token-url", default="https://login.microsoftonline.com/common/oauth2/v2.0/token")
    parser.add_argument("--complete-url", required=True)
    parser.add_argument("--hold-seconds", type=int, default=300)
    parser.add_argument("--locale", default="en-US")
    args = parser.parse_args()

    from browserforge.fingerprints import Screen
    from camoufox.sync_api import Camoufox

    profile_dir = Path(args.profile_dir)
    profile_dir.mkdir(parents=True, exist_ok=True)
    deadline = time.time() + max(60, int(args.hold_seconds or 300))

    with Camoufox(
        headless=False,
        humanize=True,
        persistent_context=True,
        user_data_dir=str(profile_dir),
        screen=Screen(max_width=1920, max_height=1080),
        proxy=proxy_config(args.proxy),
        geoip=True,
        locale=args.locale,
    ) as ctx:
        page = ctx.pages[0] if ctx.pages else ctx.new_page()
        page.goto(args.authorize_url, timeout=60000, wait_until="domcontentloaded")
        while time.time() < deadline:
            current_url = str(getattr(page, "url", "") or "")
            code, error = code_from_url(current_url)
            if error:
                raise RuntimeError(error)
            if code:
                tokens = exchange_code(args, code)
                complete = requests.post(
                    args.complete_url,
                    json={
                        "email_address": args.email,
                        "refresh_token": tokens.get("refresh_token", ""),
                        "access_token": tokens.get("access_token", ""),
                    },
                    timeout=30,
                )
                if complete.status_code < 200 or complete.status_code >= 300:
                    raise RuntimeError(complete.text)
                print(json.dumps({"ok": True, "email_address": args.email}, ensure_ascii=False))
                return 0
            page.wait_for_timeout(1000)
    raise RuntimeError("OAuth redirect code was not received before timeout")


if __name__ == "__main__":
    raise SystemExit(main())
