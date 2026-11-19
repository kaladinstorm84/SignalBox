import sys

from pack import render_pack
from review_checks import verify_review
from sections import additional_json, first_json, split_sections


def load_list(sections, label):
    body = sections.get(label)
    if not body:
        return []
    value = first_json(body)
    if not isinstance(value, list):
        raise ValueError("section '%s' must be a JSON array" % label)
    return value


def main():
    sys.stdin.reconfigure(encoding="utf-8")
    sys.stdout.reconfigure(encoding="utf-8", newline="\n")
    sys.stderr.reconfigure(encoding="utf-8")
    main_text, sections = split_sections(sys.stdin.read())
    try:
        envelope = first_json(main_text)
        requirement = additional_json(sections, "requirement")
        if not isinstance(envelope, dict) or envelope.get("kind") != "test-plan":
            raise ValueError("input does not start with a checked test plan")
        if requirement is None:
            raise ValueError("the requirement is missing from the gate input")
        outcome = next((label.split(": ", 1)[1] for label in sections if label.startswith("gate outcome: ")), None)
        if outcome not in ("approved", "escalated"):
            raise ValueError("missing '--- gate outcome: approved|escalated ---' section")
        feedback = sections["gate outcome: " + outcome]
        findings = load_list(sections, "gate findings")
        coverage = load_list(sections, "gate coverage")
        cards = load_list(sections, "gate cards")
    except (ValueError, KeyError) as exc:
        print("review pack rejected: %s" % exc, file=sys.stderr)
        return 1
    warnings = verify_review(
        envelope["plan"], requirement, findings, coverage, cards, outcome == "approved", envelope["checks"]
    )
    print(render_pack(envelope, outcome, feedback, findings, coverage, warnings))
    return 0


if __name__ == "__main__":
    sys.exit(main())
