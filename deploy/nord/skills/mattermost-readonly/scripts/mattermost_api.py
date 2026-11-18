#!/usr/bin/env python3
"""Read selected Mattermost REST endpoints using the agent's bot credentials."""

import argparse
import json
import os
import re
import sys
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode, urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener


MAX_RESPONSE_BYTES = 4 * 1024 * 1024
ID = r"[a-z0-9]{26}"
NAME = r"[a-z0-9._-]+"
READ_PATHS = re.compile(
    rf"/(?:users/(?:me|{ID}|username/{NAME})"
    rf"|users/me/(?:teams|channels|teams/{ID}/channels)"
    rf"|teams/(?:{ID}|name/{NAME}|{ID}/channels/name/{NAME})"
    rf"|channels/{ID}(?:/posts)?"
    rf"|posts/{ID}(?:/thread|/files/info)?)"
)


class MattermostError(Exception):
    pass


class NoRedirects(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise MattermostError("Mattermost redirect refused; check MATTERMOST_URL")


def read(path, params=None):
    """Return one JSON response; callers must follow the endpoint's pagination."""
    base = os.environ.get("MATTERMOST_URL", "").rstrip("/")
    token = os.environ.get("MATTERMOST_TOKEN", "")
    parsed = urlsplit(base)
    if (parsed.scheme != "https" or not parsed.hostname or parsed.username
            or parsed.password or parsed.query or parsed.fragment
            or not re.fullmatch(r"(?:/[A-Za-z0-9_-]+)*", parsed.path)):
        raise MattermostError("Set MATTERMOST_URL to the bot's HTTPS server base URL")
    if not token or any(ord(c) < 33 or ord(c) > 126 for c in token):
        raise MattermostError("Set MATTERMOST_TOKEN to the bot access token")
    if not READ_PATHS.fullmatch(path):
        raise MattermostError("Unsupported read path; use a channel, post, thread, user or team endpoint")
    query = urlencode(params or {})
    request = Request(base + "/api/v4" + path + ("?" + query if query else ""),
                      headers={"Authorization": "Bearer " + token, "Accept": "application/json"},
                      method="GET")
    try:
        with build_opener(NoRedirects()).open(request, timeout=30) as response:
            body = response.read(MAX_RESPONSE_BYTES + 1)
    except HTTPError as error:
        error.close()
        raise MattermostError(f"Mattermost HTTP {error.code}; check access or retry later") from None
    except (URLError, OSError):
        raise MattermostError("Mattermost connection failed; check the runtime's network and TLS") from None
    if len(body) > MAX_RESPONSE_BYTES:
        raise MattermostError("Mattermost response exceeds 4 MiB; request a smaller page")
    try:
        data = json.loads(body)
    except (ValueError, UnicodeError):
        raise MattermostError("Mattermost returned invalid JSON") from None
    if isinstance(data, dict) and data.get("first_inaccessible_post_time", 0):
        raise MattermostError("Mattermost reports inaccessible history; a complete summary is unavailable")
    return data


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("path", help="Read path relative to /api/v4, e.g. /users/me")
    parser.add_argument("--query", action="append", default=[], metavar="KEY=VALUE")
    args = parser.parse_args()
    params = {}
    for item in args.query:
        key, separator, value = item.partition("=")
        if not separator or not key or key in params:
            parser.error("each --query must be a unique KEY=VALUE")
        params[key] = value
    try:
        output = json.dumps(read(args.path, params), ensure_ascii=False, indent=2)
        print(output.replace(os.environ["MATTERMOST_TOKEN"], "[redacted]"))
    except MattermostError as error:
        print(str(error), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
