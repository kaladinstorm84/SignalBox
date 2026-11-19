import json
import sys

from evidence_store import ENVELOPE_MARKER, ArchiveError, archive
from sections import additional_json, first_json, split_sections


def main():
    sys.stdin.reconfigure(encoding="utf-8")
    sys.stdout.reconfigure(encoding="utf-8", newline="\n")
    sys.stderr.reconfigure(encoding="utf-8")
    pack, sections = split_sections(sys.stdin.read())
    try:
        marker = pack.find(ENVELOPE_MARKER)
        if marker < 0:
            raise ArchiveError("the input is not a review pack (embedded plan not found)")
        envelope = first_json(pack[marker + len(ENVELOPE_MARKER):])
        requirement = additional_json(sections, "requirement")
        if requirement is None:
            raise ArchiveError("the requirement is missing; add additional-input: [requirement_builder]")
        if "human decision" not in sections:
            raise ArchiveError("missing '--- human decision ---' section; archive only after a human approval")
        summary = archive(envelope, pack, requirement, sections["human decision"])
    except (ValueError, OSError) as exc:
        print("archive rejected: %s" % exc, file=sys.stderr)
        return 1
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
