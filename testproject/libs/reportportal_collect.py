#!/usr/bin/env python3
"""Fetch bounded, read-only ReportPortal evidence for one watcher trigger."""

import json
import os
import sys
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode
from urllib.request import Request, urlopen


def api_get(base_url, project, path, query, token):
    url = f"{base_url.rstrip('/')}/api/v1/{project}/{path.lstrip('/')}"
    if query:
        url = f"{url}?{urlencode(query)}"
    request = Request(url, headers={"Authorization": f"Bearer {token}", "Accept": "application/json"})
    with urlopen(request, timeout=30) as response:
        return json.load(response)


def content(value):
    return value.get("content", value.get("items", [])) if isinstance(value, dict) else value


def main():
    trigger = json.load(sys.stdin)
    token = os.environ.get("REPORTPORTAL_API_TOKEN")
    if not token:
        raise RuntimeError("REPORTPORTAL_API_TOKEN is not set for the SignalBox server process")

    base_url = trigger["base_url"]
    project = trigger["project"]
    launch_id = trigger["launch_id"]
    try:
        launch = api_get(base_url, project, f"launch/{launch_id}", {}, token)
        failed_items = api_get(base_url, project, "item", {
            "filter.eq.launchId": launch_id,
            "filter.eq.status": "FAILED",
            "page.size": 300,
            "page.sort": "startTime,ASC",
        }, token)
    except (HTTPError, URLError, json.JSONDecodeError) as error:
        raise RuntimeError(f"ReportPortal evidence collection failed: {error}") from error

    # Preserve raw, source-linked evidence. Historical test-item querying is
    # deliberately deferred until test identifiers are verified for this RP instance.
    # The configured window remains visible so reviewers know the intended scope.
    evidence = {
        "trigger": trigger,
        "launch": launch,
        "failed_items": content(failed_items),
        "history": {
            "window_days": trigger["history_days"],
            "max_runs_per_test": trigger["max_historical_runs"],
            "status": "not_collected",
            "reason": "Historical item filters require the ReportPortal test identity convention to be configured.",
        },
        "collection_limits": {"failed_item_page_size": 300, "attachments_collected": False},
    }
    print(json.dumps(evidence, separators=(",", ":")))


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print(f"reportportal collector: {error}", file=sys.stderr)
        sys.exit(1)
