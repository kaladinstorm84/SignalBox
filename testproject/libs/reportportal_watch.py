#!/usr/bin/env python3
"""Emit the newest completed failed ReportPortal launch that matches a watcher rule.

The script intentionally emits no state of its own. SignalBox persists the hash of
the last watcher output, so a launch triggers only once until a newer matching
launch becomes the result. REPORTPORTAL_API_TOKEN is read from the environment.
"""

import argparse
import json
import os
import re
import sys
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode
from urllib.request import Request, urlopen


def api_get(base_url, project, path, query, token):
    url = f"{base_url.rstrip('/')}/api/v1/{project}/{path.lstrip('/')}"
    if query:
        url = f"{url}?{urlencode(query)}"
    request = Request(url, headers={"Authorization": f"Bearer {token}", "Accept": "application/json"})
    with urlopen(request, timeout=20) as response:
        return json.load(response)


def launches_from(response):
    if isinstance(response, dict):
        return response.get("content", response.get("items", []))
    return response if isinstance(response, list) else []


def finished_at(launch):
    return launch.get("endTime") or launch.get("finishTime") or launch.get("end_time")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--project", required=True)
    parser.add_argument("--launch-name-pattern", required=True)
    parser.add_argument("--history-days", type=int, default=14)
    parser.add_argument("--max-historical-runs", type=int, default=30)
    parser.add_argument("--include-attachments", action="store_true")
    parser.add_argument("--max-launches-per-poll", type=int, default=100)
    args = parser.parse_args()

    token = os.environ.get("REPORTPORTAL_API_TOKEN")
    if not token:
        raise RuntimeError("REPORTPORTAL_API_TOKEN is not set for the SignalBox server process")
    if args.history_days < 1 or args.history_days > 90:
        raise ValueError("--history-days must be between 1 and 90")
    pattern = re.compile(args.launch_name_pattern)

    try:
        response = api_get(
            args.base_url,
            args.project,
            "launch",
            {
                "filter.eq.status": "FAILED",
                "page.size": args.max_launches_per_poll,
                "page.sort": "endTime,DESC",
            },
            token,
        )
    except (HTTPError, URLError, json.JSONDecodeError) as error:
        raise RuntimeError(f"ReportPortal launch query failed: {error}") from error

    matches = [launch for launch in launches_from(response)
               if finished_at(launch) and pattern.search(str(launch.get("name", "")))]
    if not matches:
        return
    newest = max(matches, key=lambda launch: finished_at(launch))
    launch_id = newest.get("id") or newest.get("uuid")
    if launch_id is None:
        raise RuntimeError("ReportPortal returned a launch without id or uuid")

    output = {
        "source": "reportportal",
        "base_url": args.base_url.rstrip("/"),
        "project": args.project,
        "launch_id": launch_id,
        "launch_name": newest.get("name", "Unnamed launch"),
        "launch_finished_at": finished_at(newest),
        "launch_name_pattern": args.launch_name_pattern,
        "history_days": args.history_days,
        "max_historical_runs": args.max_historical_runs,
        "include_attachments": args.include_attachments,
    }
    print(json.dumps(output, separators=(",", ":")))


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print(f"reportportal watcher: {error}", file=sys.stderr)
        sys.exit(1)
