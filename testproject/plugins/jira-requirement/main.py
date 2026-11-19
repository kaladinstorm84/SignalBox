import json
import sys

from requirement_text import MappingError, criterion_ids, flatten_rich_text, split_acceptance_criteria


def read_issue(raw):
    start = raw.find("{")
    if start < 0:
        raise MappingError("input must contain the Jira issue snapshot as a JSON object")
    try:
        issue, _ = json.JSONDecoder().raw_decode(raw[start:])
    except json.JSONDecodeError as exc:
        raise MappingError("input is not valid JSON: %s" % exc) from exc
    if not isinstance(issue, dict):
        raise MappingError("the Jira issue snapshot must be a JSON object")
    return issue


def text_field(issue, name):
    value = issue.get(name)
    if value is not None and not isinstance(value, str):
        raise MappingError("%s must be text or null" % name)
    return flatten_rich_text(value)


def build_requirement(issue):
    key = issue.get("issue_key")
    if not isinstance(key, str) or not key.strip():
        raise MappingError("issue_key is required")
    if issue.get("retrieval_complete") is not True:
        notes = "; ".join(str(n) for n in issue.get("retrieval_notes") or []) or "no reason given"
        raise MappingError("Jira retrieval was incomplete, so no plan can be trusted: " + notes)

    criteria_source = issue.get("acceptance_criteria")
    if criteria_source is not None and not isinstance(criteria_source, str):
        raise MappingError("acceptance_criteria must be text or null")
    criteria = split_acceptance_criteria(criteria_source)
    ids = criterion_ids(criteria)

    comments = []
    seen = set()
    for item in issue.get("comments") or []:
        if not isinstance(item, dict):
            raise MappingError("every comment must be an object")
        comment_id = str(item.get("id") or "").strip()
        if not comment_id:
            raise MappingError("every comment needs an id")
        if comment_id in seen:
            raise MappingError("duplicate comment id: " + comment_id)
        seen.add(comment_id)
        comments.append(
            {
                "comment_id": comment_id,
                "author": item.get("author"),
                "created_at": item.get("created"),
                "updated_at": item.get("updated"),
                "parent_id": None if item.get("parent_id") is None else str(item["parent_id"]),
                "restricted": bool(item.get("restricted")),
                "internal": bool(item.get("internal")),
                "body": flatten_rich_text(item.get("body")),
            }
        )

    summary = text_field(issue, "summary")
    description = text_field(issue, "description")

    references = dict(zip(ids, criteria))
    if summary.strip():
        references["SUMMARY"] = summary
    if description.strip():
        references["DESCRIPTION"] = description
    for comment in comments:
        if comment["body"].strip():
            references["COMMENT:" + comment["comment_id"]] = comment["body"]
    if not references:
        raise MappingError("the issue has no summary, description, acceptance criteria or comments to plan from")

    notes = []
    if not criteria:
        notes.append(
            "Acceptance Criteria field is empty; inspect the description and comments for testable behaviour."
        )

    return {
        "kind": "requirement",
        "issue_key": key.strip(),
        "source_url": issue.get("source_url"),
        "summary": summary,
        "description": description,
        "labels": [str(label) for label in issue.get("labels") or []],
        "acceptance_criteria": criteria,
        "acceptance_criterion_ids": ids,
        "comments": comments,
        "source_references": references,
        "input_notes": notes,
    }


def main():
    sys.stdin.reconfigure(encoding="utf-8")
    sys.stdout.reconfigure(encoding="utf-8", newline="\n")
    sys.stderr.reconfigure(encoding="utf-8")
    try:
        requirement = build_requirement(read_issue(sys.stdin.read()))
    except MappingError as exc:
        print("requirement rejected: %s" % exc, file=sys.stderr)
        return 1
    print(json.dumps(requirement, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
