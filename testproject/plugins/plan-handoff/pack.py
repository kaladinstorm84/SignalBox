"""Render the Markdown a human reviews; the exact plan is embedded for the revision and archive steps."""

import json
import re

ENVELOPE_MARKER = "<!-- signalbox:plan-envelope -->"
_LIMIT = re.compile(r"rework limit \((\d+)\)")


def _bullets(items, empty="None."):
    return "\n".join("- " + item for item in items) if items else empty


def _cell(text):
    return str(text).replace("|", "\\|").replace("\n", " ")


def outcome_summary(approved, feedback, findings):
    """One sentence saying why the plan reached a human."""
    if approved:
        return "The AI review found no issues. This is not an approval; the decision is yours."
    limit = _LIMIT.search(feedback or "")
    if limit:
        return (
            "The AI review could not resolve every finding within %s revision(s). "
            "%d finding(s) remain." % (limit.group(1), len(findings))
        )
    return "The AI review needs a product decision that only a person can make."


def render_pack(envelope, outcome, feedback, findings, coverage, warnings):
    plan = envelope["plan"]
    checks = envelope["checks"]
    approved = outcome == "approved"
    needs_human = [f for f in findings if f.get("requires_human_clarification")]
    lines = [
        "# %s \u2014 %s" % (plan["issue_key"], plan["title"]),
        "",
        "Plan `%s` \u00b7 revision %d \u00b7 %d scenario(s)" % (plan["plan_id"], plan["revision"], len(plan["scenarios"])),
        "",
        "**%s**" % outcome_summary(approved, feedback, findings),
        "",
        "Approve to archive exactly this plan. Reject with a comment to request changes: your comment "
        "is given to the reviser as authoritative feedback, and the AI review runs again.",
        "",
        "## Needs your attention",
        "",
        "### Open questions",
        _bullets(plan["open_questions"]),
        "",
        "### Findings that need a human decision",
        _bullets(["**%s** %s" % (f["id"], f["problem"]) for f in needs_human]),
    ]
    if checks["uncited_acceptance_criteria"]:
        lines += ["", "### Acceptance criteria no scenario cites", _bullets(checks["uncited_acceptance_criteria"])]
    if warnings:
        lines += ["", "### Review claims that could not be verified", _bullets(warnings)]
    if plan["input_notes"]:
        lines += ["", "### Input notes", _bullets(plan["input_notes"])]

    lines += ["", "## AI review", "", feedback or "No feedback.", ""]
    if findings:
        lines += ["| Id | Severity | Category | Scenarios | Problem | Suggested change |", "|---|---|---|---|---|---|"]
        for f in findings:
            scope = ", ".join(str(n) for n in f.get("scenario_numbers", [])) or "plan-wide"
            lines.append(
                "| %s | %s | %s | %s | %s | %s |"
                % (f["id"], f["severity"], f["category"], scope, _cell(f["problem"]), _cell(f["suggested_change"]))
            )
    if coverage:
        lines += ["", "### Coverage", "", "| Source | Status | Scenarios | Assessment |", "|---|---|---|---|"]
        for row in coverage:
            scope = ", ".join(str(n) for n in row.get("scenario_numbers", [])) or "\u2014"
            lines.append(
                "| %s | %s | %s | %s |"
                % (row["source_reference_id"], row["status"], scope, _cell(row["assessment"]))
            )

    lines += ["", "## Plan", "", "**Objective.** " + plan["objective"], "", "### Assumptions", _bullets(plan["assumptions"])]
    if plan["revision_changes"]:
        lines += ["", "### Changes in this revision", _bullets(plan["revision_changes"])]
    if plan.get("human_feedback"):
        lines += ["", "### Human feedback on record", _bullets(["`%s` %s" % (e["reference"], e["text"]) for e in plan["human_feedback"]])]
    for scenario in plan["scenarios"]:
        lines += [
            "",
            "### %d. %s" % (scenario["number"], scenario["title"]),
            "",
            "Layer: `%s` \u00b7 Sources: %s" % (scenario["recommended_test_layer"], ", ".join(scenario["source_reference_ids"])),
            "",
            "**Preconditions**",
            _bullets(scenario["preconditions"], "None."),
            "",
            "**Actions**",
            "\n".join("%d. %s" % (i, step) for i, step in enumerate(scenario["actions"], 1)),
            "",
            "**Expected results**",
            _bullets(scenario["expected_results"]),
        ]
    lines += [
        "",
        "---",
        "",
        "Machine-readable plan (used by the revision and archive steps; do not edit):",
        "",
        ENVELOPE_MARKER,
        "```json",
        json.dumps(envelope, ensure_ascii=False, indent=2),
        "```",
        "",
    ]
    return "\n".join(lines)
