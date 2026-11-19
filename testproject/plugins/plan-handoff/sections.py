"""Parsing helpers for the labelled sections SignalBox appends to plugin input."""

import json
import re

_MARKER = re.compile(r"^--- (.+?) ---[ \t\r]*$", re.MULTILINE)


def split_sections(text):
    """Split text on '--- label ---' lines into (main, {label: body})."""
    matches = list(_MARKER.finditer(text))
    if not matches:
        return text.strip(), {}
    main = text[: matches[0].start()].strip()
    sections = {}
    for match, following in zip(matches, matches[1:] + [None]):
        end = following.start() if following else len(text)
        sections[match.group(1)] = text[match.end():end].strip()
    return main, sections


def first_json(text):
    """Decode the first JSON object or array in text, ignoring surrounding prose."""
    starts = [i for i in (text.find("{"), text.find("[")) if i >= 0]
    if not starts:
        raise ValueError("no JSON document found")
    try:
        value, _ = json.JSONDecoder().raw_decode(text[min(starts):])
    except json.JSONDecodeError as exc:
        raise ValueError("invalid JSON: %s" % exc) from exc
    return value


def additional_json(sections, kind):
    """Return the additional-input section whose JSON has the given kind, or None."""
    for label, body in sections.items():
        if not label.startswith("additional input from "):
            continue
        try:
            value = first_json(body)
        except ValueError:
            continue
        if isinstance(value, dict) and value.get("kind") == kind:
            return value
    return None
