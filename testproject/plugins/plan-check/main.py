import json
import sys

from plan_rules import PlanError, build_plan
from sections import additional_json, first_json, split_sections


def main():
    sys.stdin.reconfigure(encoding="utf-8")
    sys.stdout.reconfigure(encoding="utf-8", newline="\n")
    sys.stderr.reconfigure(encoding="utf-8")
    main_text, sections = split_sections(sys.stdin.read())
    requirement = additional_json(sections, "requirement")
    if requirement is None:
        print("plan rejected: the requirement is missing; add additional-input: [requirement_builder]", file=sys.stderr)
        return 1
    try:
        draft = first_json(main_text)
        status, plan, checks = build_plan(draft, requirement)
    except ValueError as exc:
        details = getattr(exc, "errors", [str(exc)])
        print("plan rejected:\n" + "\n".join("- " + detail for detail in details), file=sys.stderr)
        return 1
    print(json.dumps({"kind": "test-plan", "status": status, "plan": plan, "checks": checks}, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
