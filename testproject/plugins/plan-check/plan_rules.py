"""Validation and identity rules for a drafted or revised test plan."""

import json
import re
import uuid
from datetime import datetime, timezone

LAYERS = ("ui_e2e", "api", "hybrid", "manual")
REVISION_KINDS = ("ai_review", "human_feedback")
FEEDBACK_REFERENCE = re.compile(r"^FEEDBACK:R([0-9]+)$")


class PlanError(ValueError):
    """The plan breaks a rule that no reviewer should be asked to judge."""

    def __init__(self, errors):
        super().__init__("; ".join(errors))
        self.errors = errors


def _text_list(value, label, errors, minimum=0):
    if not isinstance(value, list) or not all(isinstance(item, str) and item.strip() for item in value):
        errors.append("%s must be a list of non-empty text" % label)
        return []
    if len(value) < minimum:
        errors.append("%s needs at least %d item(s)" % (label, minimum))
    return [item.strip() for item in value]


def _normal(items):
    return tuple(" ".join(item.split()).casefold() for item in items)


def build_plan(draft, requirement):
    """Return (status, plan, checks); raise PlanError listing every violation."""
    errors = []
    if not isinstance(draft, dict):
        raise PlanError(["the plan must be a JSON object"])

    revision = "based_on_plan_id" in draft
    kind = draft.get("revision_kind")
    parent = None
    number = 0
    if revision:
        parent = draft.get("based_on_plan_id")
        number = draft.get("based_on_revision")
        if not isinstance(parent, str) or not parent.strip():
            errors.append("based_on_plan_id must be the plan_id being revised")
        if not isinstance(number, int) or isinstance(number, bool) or number < 0:
            errors.append("based_on_revision must be a non-negative integer")
            number = 0
        if kind not in REVISION_KINDS:
            errors.append("revision_kind must be one of %s" % ", ".join(REVISION_KINDS))
        number += 1

    title = draft.get("title")
    objective = draft.get("objective")
    for name, value in (("title", title), ("objective", objective)):
        if not isinstance(value, str) or not value.strip():
            errors.append("%s must be non-empty text" % name)
    assumptions = _text_list(draft.get("assumptions", []), "assumptions", errors)
    questions = _text_list(draft.get("open_questions", []), "open_questions", errors)
    changes = _text_list(draft.get("changes", []), "changes", errors) if revision else []
    feedback = _feedback(draft.get("human_feedback", []), kind if revision else None, number, errors)
    feedback_refs = {entry["reference"] for entry in feedback}

    raw_scenarios = draft.get("scenarios")
    if not isinstance(raw_scenarios, list):
        errors.append("scenarios must be a list")
        raw_scenarios = []

    known = requirement["source_references"]
    criteria = set(requirement["acceptance_criterion_ids"])
    warnings = []
    scenarios = []
    seen = {}
    for position, raw in enumerate(raw_scenarios, 1):
        label = "scenario %d" % position
        if not isinstance(raw, dict):
            errors.append("%s must be an object" % label)
            continue
        scenario_title = raw.get("title")
        if not isinstance(scenario_title, str) or not scenario_title.strip():
            errors.append("%s needs a title" % label)
        refs = _text_list(raw.get("source_reference_ids"), label + " source_reference_ids", errors, 1)
        unique = list(dict.fromkeys(refs))
        if len(unique) != len(refs):
            warnings.append("%s cited a source reference more than once; duplicates were removed" % label)
        for ref in unique:
            if ref not in known and ref not in feedback_refs:
                errors.append("%s cites unknown source reference %s" % (label, ref))
        preconditions = _text_list(raw.get("preconditions", []), label + " preconditions", errors)
        actions = _text_list(raw.get("actions"), label + " actions", errors, 1)
        results = _text_list(raw.get("expected_results"), label + " expected_results", errors, 1)
        layer = raw.get("recommended_test_layer")
        if layer not in LAYERS:
            errors.append("%s recommended_test_layer must be one of %s" % (label, ", ".join(LAYERS)))
        fingerprint = (_normal(actions), _normal(results))
        if all(fingerprint) and fingerprint in seen:
            errors.append("%s duplicates scenario %d (same actions and expected results)" % (label, seen[fingerprint]))
        seen.setdefault(fingerprint, position)
        scenarios.append(
            {
                "number": position,
                "title": (scenario_title or "").strip(),
                "acceptance_criterion_ids": [ref for ref in unique if ref in criteria],
                "source_reference_ids": unique,
                "preconditions": preconditions,
                "actions": actions,
                "expected_results": results,
                "recommended_test_layer": layer,
            }
        )

    if not scenarios:
        if revision:
            errors.append("a revision must keep at least one scenario")
        elif not questions:
            errors.append("no scenarios and no open_questions: say what must be clarified")
    if errors:
        raise PlanError(errors)

    cited = {ref for scenario in scenarios for ref in scenario["source_reference_ids"]}
    checks = {
        "scenario_count": len(scenarios),
        "uncited_acceptance_criteria": [ref for ref in requirement["acceptance_criterion_ids"] if ref not in cited],
        "uncited_other_sources": [ref for ref in known if ref not in criteria and ref not in cited],
        "warnings": warnings,
    }
    plan = {
        "plan_id": _plan_id(requirement["issue_key"], parent, title, objective, assumptions, questions, feedback, scenarios),
        "revision": number,
        "parent_plan_id": parent,
        "revision_kind": kind if revision else None,
        "revision_changes": changes,
        "human_feedback": feedback,
        "created_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "issue_key": requirement["issue_key"],
        "title": title.strip(),
        "objective": objective.strip(),
        "assumptions": assumptions,
        "open_questions": questions,
        "input_notes": list(requirement.get("input_notes") or []),
        "scenarios": scenarios,
    }
    return ("ready" if scenarios else "needs_clarification"), plan, checks


def _feedback(raw, kind, revision_number, errors):
    """Validate the human feedback record: FEEDBACK:R<n> entries, one per human round, n <= this revision.

    Only a human_feedback revision may add the entry for its own revision, and it must.
    Earlier entries are echoed by the reviser and cannot be verified here.
    """
    if not isinstance(raw, list):
        errors.append("human_feedback must be a list")
        return []
    entries = {}
    for position, item in enumerate(raw, 1):
        reference = item.get("reference") if isinstance(item, dict) else None
        text = item.get("text") if isinstance(item, dict) else None
        match = FEEDBACK_REFERENCE.match(reference) if isinstance(reference, str) else None
        if not match or not 1 <= int(match.group(1)) <= revision_number:
            errors.append("human_feedback item %d needs a reference FEEDBACK:R<n> with n no higher than revision %d" % (position, revision_number))
        elif not isinstance(text, str) or not text.strip():
            errors.append("human_feedback %s needs the reviewer's text" % reference)
        elif reference in entries:
            errors.append("human_feedback %s appears more than once" % reference)
        else:
            entries[reference] = text.strip()
    current = "FEEDBACK:R%d" % revision_number
    if kind == "human_feedback" and current not in entries:
        errors.append("a human_feedback revision must record the reviewer's comment as %s" % current)
    if kind != "human_feedback" and current in entries:
        errors.append("only a human_feedback revision can add %s" % current)
    ordered = sorted(entries, key=lambda ref: int(FEEDBACK_REFERENCE.match(ref).group(1)))
    return [{"reference": ref, "text": entries[ref]} for ref in ordered]


def _plan_id(issue_key, parent, *content):
    canonical = json.dumps([issue_key, parent, content], sort_keys=True, ensure_ascii=False)
    return "plan-" + str(uuid.uuid5(uuid.NAMESPACE_URL, canonical))
