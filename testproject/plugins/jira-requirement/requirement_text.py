"""Deterministic Jira text normalisation: rich-text flattening and acceptance-criteria splitting."""

import re
from html.parser import HTMLParser

IGNORED_TAGS = frozenset({"script", "style"})
BLOCK_TAGS = frozenset(
    {
        "address", "article", "aside", "blockquote", "br", "div",
        "h1", "h2", "h3", "h4", "h5", "h6", "li", "p", "pre", "section", "tr",
    }
)
AC_LABEL = re.compile(r"(?im)^[ \t]*AC[ \t]*-?[ \t]*([0-9]+)(?=[ \t:.)\-]|$)")
LIST_PREFIX = re.compile("^(?:[-*\u2022]\\s+|[0-9]+[.)]\\s+)")


class MappingError(ValueError):
    """A source problem a person has to fix in Jira before a plan can be generated."""


def _clean_text(parts):
    lines = []
    for line in "".join(parts).splitlines():
        normalised = " ".join(line.split())
        if normalised:
            lines.append(normalised)
    return "\n".join(lines)


class _RichTextExtractor(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self._parts = []
        self._ignored = 0

    @property
    def text(self):
        return _clean_text(self._parts)

    def handle_starttag(self, tag, attrs):
        if tag in IGNORED_TAGS:
            self._ignored += 1
        elif not self._ignored and tag in BLOCK_TAGS:
            self._parts.append("\n")

    def handle_endtag(self, tag):
        if tag in IGNORED_TAGS and self._ignored:
            self._ignored -= 1
        elif not self._ignored and tag in BLOCK_TAGS:
            self._parts.append("\n")

    def handle_data(self, data):
        if not self._ignored:
            self._parts.append(data)


class _ListItemExtractor(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.items = []
        self._parts = []
        self._depth = 0
        self._ignored = 0

    def handle_starttag(self, tag, attrs):
        if tag in IGNORED_TAGS:
            self._ignored += 1
            return
        if self._ignored:
            return
        if tag == "li":
            if self._depth == 0:
                self._parts = []
            self._depth += 1
        elif self._depth and tag in BLOCK_TAGS:
            self._parts.append(" ")

    def handle_endtag(self, tag):
        if tag in IGNORED_TAGS and self._ignored:
            self._ignored -= 1
            return
        if self._ignored or tag != "li" or self._depth == 0:
            return
        self._depth -= 1
        if self._depth == 0:
            item = " ".join("".join(self._parts).split())
            if item:
                self.items.append(item)

    def handle_data(self, data):
        if not self._ignored and self._depth:
            self._parts.append(data)


def flatten_rich_text(value):
    """Flatten Jira HTML/markup to plain text, keeping block boundaries as newlines."""
    if value is None:
        return ""
    parser = _RichTextExtractor()
    parser.feed(value)
    parser.close()
    return parser.text


def split_acceptance_criteria(value):
    """Split the Acceptance Criteria field: AC-n headings, then HTML list items, then list-prefixed lines."""
    if value is None or not value.strip():
        return []
    text = flatten_rich_text(value)
    labelled = _split_labelled(text)
    if labelled:
        return labelled
    items = _ListItemExtractor()
    items.feed(value)
    items.close()
    if items.items:
        return items.items
    groups = []
    for line in text.splitlines():
        if LIST_PREFIX.match(line) or not groups:
            groups.append(LIST_PREFIX.sub("", line).strip())
        else:
            groups[-1] += "\n" + line
    return [group for group in groups if group.strip()]


def _split_labelled(text):
    matches = list(AC_LABEL.finditer(text))
    if not matches:
        return []
    if text[: matches[0].start()].strip():
        raise MappingError(
            "Acceptance Criteria contains unlabelled content before an AC label; "
            "clarify criterion boundaries before generating a plan"
        )
    starts = [match.start() for match in matches] + [len(text)]
    return [text[start:end].strip() for start, end in zip(starts, starts[1:])]


def criterion_ids(criteria):
    """Keep source AC numbers; unlabelled criteria become GENERATED-AC-<position>."""
    ids = []
    for index, criterion in enumerate(criteria, 1):
        match = AC_LABEL.match(criterion)
        if match and int(match[1]) == 0:
            raise MappingError("Explicit AC numbering must start at 1")
        reference = "AC-%d" % int(match[1]) if match else "GENERATED-AC-%d" % index
        if reference in ids:
            raise MappingError(
                "Acceptance criterion identifiers must be unique: %s; "
                "clarify duplicate source labels before generating a plan" % reference
            )
        ids.append(reference)
    return ids
