"""Deterministic checks of an AI review report against the plan and requirement it reviewed."""

CRITERIA = (
    "source_fidelity", "observable_outcomes", "test_data", "scenario_coherence",
    "duplication", "ambiguity", "revision_integrity",
)


def _squash(text):
    return " ".join(str(text).split())


def plan_text(plan):
    """All reviewable plan text, whitespace-normalised, for verbatim excerpt checks."""
    parts = [plan["title"], plan["objective"]]
    parts += plan["assumptions"] + plan["open_questions"]
    for scenario in plan["scenarios"]:
        parts.append(scenario["title"])
        for key in ("preconditions", "actions", "expected_results"):
            parts += scenario[key]
    return _squash("\n".join(parts))


def verify_review(plan, requirement, findings, coverage, cards, approved, checks):
    """Return human-readable warnings for every claim the report makes that cannot be confirmed."""
    problems = []
    total = len(plan["scenarios"])
    references = dict(requirement["source_references"])
    references.update({entry["reference"]: entry["text"] for entry in plan.get("human_feedback", [])})
    finding_ids = [finding.get("id") for finding in findings]
    known_findings = set(finding_ids)
    linked = set()
    haystack = plan_text(plan)

    if len(known_findings) != len(finding_ids):
        problems.append("Finding ids are not unique.")

    def in_range(numbers):
        return all(isinstance(n, int) and 1 <= n <= total for n in numbers)

    for finding in findings:
        fid = finding.get("id")
        if not in_range(finding.get("scenario_numbers", [])):
            problems.append("%s names a scenario number outside 1-%d." % (fid, total))
        for ref in finding.get("source_reference_ids", []):
            if ref not in references:
                problems.append("%s cites unknown source reference %s." % (fid, ref))
        if not finding.get("source_reference_ids") and not finding.get("plan_excerpts"):
            problems.append("%s has neither a source reference nor a plan excerpt." % fid)
        for excerpt in finding.get("plan_excerpts", []):
            if _squash(excerpt) not in haystack:
                problems.append("%s quotes text that is not in the plan: \u201c%s\u201d" % (fid, _squash(excerpt)[:80]))

    covered_references = set()
    for row in coverage:
        ref = row.get("source_reference_id")
        status = row.get("status")
        covered_references.add(ref)
        linked.update(row.get("finding_ids", []))
        if ref not in references:
            problems.append("Coverage row for unknown source reference %s." % ref)
            continue
        source = _squash(references[ref])
        excerpt = _squash(row.get("requirement_excerpt", ""))
        if excerpt not in source:
            problems.append("Coverage row %s quotes text that is not in the requirement." % ref)
        if ref in requirement["acceptance_criterion_ids"]:
            if status == "not_applicable":
                problems.append("Acceptance criterion %s was marked not_applicable." % ref)
            elif excerpt != source and status == "covered":
                problems.append("Acceptance criterion %s is covered but its row does not quote the complete text." % ref)
        numbers = row.get("scenario_numbers", [])
        if not in_range(numbers):
            problems.append("Coverage row %s names a scenario number outside 1-%d." % (ref, total))
        if status in ("covered", "partial") and not numbers:
            problems.append("Coverage row %s is %s but cites no scenario." % (ref, status))
        if status in ("partial", "blocked", "not_covered") and not row.get("finding_ids"):
            problems.append("Coverage row %s is %s but has no finding." % (ref, status))
        if any(fid not in known_findings for fid in row.get("finding_ids", [])):
            problems.append("Coverage row %s links a finding that does not exist." % ref)
    for ref in references:
        if ref not in covered_references:
            problems.append("Source reference %s is missing from the coverage inventory." % ref)

    carded = set()
    for card in cards:
        number = card.get("scenario_number")
        carded.add(number)
        if not in_range([number]):
            problems.append("A scenario card names scenario %s, which does not exist." % number)
        for name in CRITERIA:
            criterion = card.get("criteria", {}).get(name, {})
            linked.update(criterion.get("finding_ids", []))
            if criterion.get("status") in ("issue_found", "cannot_assess") and not criterion.get("finding_ids"):
                problems.append("Scenario %s %s is %s without a finding." % (number, name, criterion.get("status")))
            if any(fid not in known_findings for fid in criterion.get("finding_ids", [])):
                problems.append("Scenario %s %s links a finding that does not exist." % (number, name))
    for number in range(1, total + 1):
        if number not in carded:
            problems.append("Scenario %d has no assessment card." % number)

    for finding in findings:
        if finding.get("id") not in linked:
            problems.append("%s is not linked from any scenario card or coverage row." % finding.get("id"))

    if approved and findings:
        problems.append("The review approved the plan but still reports %d finding(s)." % len(findings))
    if approved and checks.get("uncited_acceptance_criteria"):
        problems.append(
            "The review approved the plan, but no scenario cites: %s."
            % ", ".join(checks["uncited_acceptance_criteria"])
        )
    return problems
