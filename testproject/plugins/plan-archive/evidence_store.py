"""Write-once evidence storage for an approved test plan."""

import hashlib
import json
import os
import re
import shutil
import tempfile
from datetime import datetime, timezone
from pathlib import Path

EVIDENCE_ROOT = Path("evidence") / "testplans"
ENVELOPE_MARKER = "<!-- signalbox:plan-envelope -->"
ISSUE_KEY = re.compile(r"^[A-Za-z][A-Za-z0-9_]*-[0-9]+$")
PLAN_ID = re.compile(r"^plan-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")


class ArchiveError(ValueError):
    """Evidence cannot be stored without breaking write-once guarantees."""


def _sha256(data):
    return hashlib.sha256(data).hexdigest()


def _bytes(value):
    return value.encode("utf-8")


def _json_bytes(value):
    return _bytes(json.dumps(value, ensure_ascii=False, indent=2) + "\n")


def archive(envelope, pack, requirement, comment, root=EVIDENCE_ROOT):
    """Store the approved plan; return a summary. An identical re-run is a no-op."""
    plan = envelope.get("plan") or {}
    if envelope.get("kind") != "test-plan" or envelope.get("status") != "ready" or not plan.get("scenarios"):
        raise ArchiveError("only a ready plan with scenarios can be archived")
    if not ISSUE_KEY.match(str(plan.get("issue_key"))) or not PLAN_ID.match(str(plan.get("plan_id"))):
        raise ArchiveError("the plan has an invalid issue_key or plan_id")

    target = Path(root) / plan["issue_key"] / plan["plan_id"]
    content = {
        "plan.json": _json_bytes(plan),
        "review-pack.md": _bytes(pack.rstrip() + "\n"),
        "requirement.json": _json_bytes(requirement),
    }
    if target.exists():
        for name, data in content.items():
            existing = target / name
            if not existing.is_file() or existing.read_bytes() != data:
                raise ArchiveError("evidence for %s already exists with different content; it is write-once" % plan["plan_id"])
        return _summary(plan, target, "already_archived")

    decision = {
        "decision": "approved",
        "plan_id": plan["plan_id"],
        "comment": comment,
        "decided_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
    }
    content["decision.json"] = _json_bytes(decision)
    content["manifest.json"] = _json_bytes({name: _sha256(data) for name, data in sorted(content.items())})

    target.parent.mkdir(parents=True, exist_ok=True)
    staging = Path(tempfile.mkdtemp(prefix=".staging-", dir=target.parent))
    try:
        for name, data in content.items():
            (staging / name).write_bytes(data)
            os.chmod(staging / name, 0o444)
        try:
            os.rename(staging, target)
        except OSError as exc:
            raise ArchiveError("could not publish evidence for %s: %s" % (plan["plan_id"], exc)) from exc
    finally:
        if staging.exists():
            shutil.rmtree(staging, ignore_errors=True)
    return _summary(plan, target, "archived")


def _summary(plan, target, status):
    manifest = json.loads((target / "manifest.json").read_text(encoding="utf-8"))
    return {
        "status": status,
        "issue_key": plan["issue_key"],
        "plan_id": plan["plan_id"],
        "revision": plan["revision"],
        "scenarios": len(plan["scenarios"]),
        "path": target.as_posix(),
        "files": manifest,
    }
