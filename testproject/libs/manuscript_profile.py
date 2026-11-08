"""Manuscript profile for the manuscript-review pipeline.

Reads the single uploaded manuscript (.txt, .md or .docx) from
SIGNALBOX_FILES_DIR and prints a Markdown profile: length, chapter map and
prose heuristics. The author's brief (the run input on stdin) is repeated at
the top so downstream editors receive it with the profile.

The figures are heuristics to direct an editor's attention, not judgments.
Only the Python standard library is used.
"""
import collections
import os
import re
import statistics
import sys
import zipfile
from xml.etree import ElementTree

SUPPORTED = (".txt", ".md", ".docx")
WORDS_PER_PAGE = 250
WORDS_PER_MINUTE = 250

WORD = re.compile(r"[A-Za-z0-9]+(?:['\u2019-][A-Za-z0-9]+)*")
HEADING = re.compile(
    r"^\s*(?:#{1,3}\s+\S.*|(?:chapter|prologue|epilogue|part|interlude|book)\b.*)$",
    re.IGNORECASE,
)
SENTENCE_END = re.compile(r"(?<=[.!?])[\"'\u201d\u2019)]*\s+")
DIALOGUE = re.compile(r"\u201c[^\u201d]*\u201d|\"[^\"\n]*\"")
FILTER_WORDS = {
    "felt", "feel", "feels", "saw", "see", "sees", "heard", "hear", "hears",
    "noticed", "notice", "realized", "realised", "seemed", "seems", "wondered",
    "watched", "thought", "knew", "decided",
}
NOT_ADVERBS = {
    "only", "family", "early", "reply", "holy", "ugly", "likely", "lonely",
    "lovely", "silly", "belly", "jelly", "fly", "apply", "supply", "rely",
    "italy", "july", "daily", "friendly", "elderly", "costly", "curly", "bully",
    "lily", "sally", "molly", "emily", "kelly", "holly", "ally", "rally",
}
STOPWORDS = set(
    """a about above after again against all also am an and any are as at be
    because been before being below between both but by can could did do does
    doing down during each few for from further had has have having he her here
    hers herself him himself his how i if in into is it its itself just me more
    most my myself no nor not now of off on once only or other our ours out over
    own same she should so some such than that the their theirs them then there
    these they this those through to too under until up very was we were what
    when where which while who whom why will with would you your yours yourself
    said says one back like into could would there their them then just""".split()
)
DOCX_NS = "{http://schemas.openxmlformats.org/wordprocessingml/2006/main}"


def fail(message):
    print(message, file=sys.stderr)
    sys.exit(1)


def read_text(path):
    raw = open(path, "rb").read()
    for encoding in ("utf-8-sig", "cp1252"):
        try:
            return raw.decode(encoding)
        except UnicodeDecodeError:
            continue
    return raw.decode("latin-1")


def read_docx(path):
    """Returns paragraphs, marking Word heading styles as headings."""
    try:
        with zipfile.ZipFile(path) as archive:
            xml = archive.read("word/document.xml")
    except (zipfile.BadZipFile, KeyError):
        fail("The .docx file could not be read; save it again from your word processor.")
    paragraphs = []
    for para in ElementTree.fromstring(xml).iter(DOCX_NS + "p"):
        text = "".join(node.text or "" for node in para.iter(DOCX_NS + "t")).strip()
        if not text:
            continue
        style = para.find(f"{DOCX_NS}pPr/{DOCX_NS}pStyle")
        style_name = style.get(DOCX_NS + "val", "") if style is not None else ""
        paragraphs.append((text, style_name.lower().startswith(("heading", "title"))))
    return paragraphs


def text_paragraphs(text):
    blocks = re.split(r"\n\s*\n", text.replace("\r\n", "\n"))
    paragraphs = []
    for block in blocks:
        block = block.strip()
        if not block:
            continue
        # A heading may sit directly above its first paragraph.
        lines = block.split("\n")
        if len(lines) > 1 and is_heading(lines[0]):
            paragraphs.append((lines[0].strip(), True))
            block = "\n".join(lines[1:]).strip()
        paragraphs.append((" ".join(block.split()), is_heading(block)))
    return paragraphs


def is_heading(line):
    line = line.strip()
    return "\n" not in line and len(line) <= 80 and bool(HEADING.match(line))


def heading_title(text):
    return text.lstrip("#").strip()


def split_chapters(paragraphs):
    chapters = []
    current = {"title": "(opening, before the first heading)", "paragraphs": []}
    for text, heading in paragraphs:
        if heading:
            if current["paragraphs"] or chapters:
                chapters.append(current)
            current = {"title": heading_title(text), "paragraphs": []}
        else:
            current["paragraphs"].append(text)
    chapters.append(current)
    return [c for c in chapters if c["paragraphs"]]


def words_of(text):
    return WORD.findall(text)


def snippet(text, count=12):
    words = text.split()
    return " ".join(words[:count]) + (" ..." if len(words) > count else "")


def per_thousand(count, total):
    return round(count * 1000 / total, 1) if total else 0


def main():
    brief = sys.stdin.read().strip()
    files_dir = os.environ.get("SIGNALBOX_FILES_DIR", "")
    if not files_dir:
        fail("No manuscript was attached (SIGNALBOX_FILES_DIR is not set).")
    names = sorted(n for n in os.listdir(files_dir) if os.path.isfile(os.path.join(files_dir, n)))
    manuscripts = [n for n in names if n.lower().endswith(SUPPORTED)]
    if len(manuscripts) != 1:
        fail(f"Attach exactly one manuscript ({', '.join(SUPPORTED)}); found: {', '.join(names) or 'none'}.")
    name = manuscripts[0]
    path = os.path.join(files_dir, name)
    paragraphs = read_docx(path) if name.lower().endswith(".docx") else text_paragraphs(read_text(path))
    chapters = split_chapters(paragraphs)
    body = [p for c in chapters for p in c["paragraphs"]]
    all_words = [w for p in body for w in words_of(p)]
    total = len(all_words)
    if total == 0:
        fail(f"{name} contains no readable prose.")

    lower = [w.lower() for w in all_words]
    sentences = []
    for chapter in chapters:
        for para in chapter["paragraphs"]:
            for sentence in SENTENCE_END.split(para):
                count = len(words_of(sentence))
                if count:
                    sentences.append((count, sentence.strip(), chapter["title"]))
    lengths = [s[0] for s in sentences]
    dialogue_words = sum(len(words_of(m)) for p in body for m in DIALOGUE.findall(p))
    adverbs = [w for w in lower if w.endswith("ly") and len(w) > 4 and w not in NOT_ADVERBS]
    filters = collections.Counter(w for w in lower if w in FILTER_WORDS)
    content = collections.Counter(w for w in lower if len(w) > 3 and w not in STOPWORDS)
    openers = collections.Counter(words_of(s[1])[0].lower() for s in sentences if words_of(s[1]))
    chapter_words = [(c["title"], sum(len(words_of(p)) for p in c["paragraphs"])) for c in chapters]
    median_chapter = statistics.median(w for _, w in chapter_words)
    long_paragraphs = [
        (c["title"], len(words_of(p)), p) for c in chapters for p in c["paragraphs"] if len(words_of(p)) > 250
    ]
    longest = sorted(sentences, key=lambda s: s[0], reverse=True)[:3]

    out = []
    out.append("## Author brief")
    out.append(brief or "(No brief supplied. Infer intent from the manuscript and say so.)")
    out.append("")
    out.append(f"## Manuscript profile: {name}")
    out.append("_Heuristic figures to direct attention. They are not quality judgments._")
    out.append("")
    out.append(f"- Words: {total:,}")
    out.append(f"- Estimated pages: {max(1, round(total / WORDS_PER_PAGE))} (at {WORDS_PER_PAGE} words/page)")
    out.append(f"- Estimated reading time: {max(1, round(total / WORDS_PER_MINUTE))} minutes")
    out.append(f"- Paragraphs: {len(body):,}; sentences: {len(sentences):,}")
    out.append(
        f"- Sentence length: mean {statistics.mean(lengths):.1f} words, median "
        f"{statistics.median(lengths):.0f}, longest {max(lengths)}"
    )
    out.append(f"- Dialogue share: {dialogue_words * 100 / total:.0f}% of words appear inside quotation marks")
    out.append(f"- -ly adverbs: {len(adverbs)} ({per_thousand(len(adverbs), total)} per 1,000 words)")
    out.append(
        f"- Filter words (felt, saw, noticed, realized, ...): {sum(filters.values())} "
        f"({per_thousand(sum(filters.values()), total)} per 1,000 words)"
    )
    out.append("")
    out.append("### Chapter map")
    if len(chapter_words) == 1 and chapters[0]["title"].startswith("("):
        out.append("No chapter headings were detected.")
    out.append("| # | Chapter | Words | Note |")
    out.append("|---|---------|-------|------|")
    for i, (title, words) in enumerate(chapter_words, 1):
        note = ""
        if len(chapter_words) > 2 and words > 2 * median_chapter:
            note = "over twice the median length"
        elif len(chapter_words) > 2 and words < 0.4 * median_chapter:
            note = "under 40% of the median length"
        out.append(f"| {i} | {title} | {words:,} | {note} |")
    out.append("")
    out.append("### Most frequent content words")
    out.append(", ".join(f"{w} ({n})" for w, n in content.most_common(15)) or "(none)")
    out.append("")
    out.append("### Most frequent sentence openers")
    out.append(", ".join(f"{w} ({n})" for w, n in openers.most_common(8)))
    out.append("")
    out.append("### Most frequent filter words")
    out.append(", ".join(f"{w} ({n})" for w, n in filters.most_common(8)) or "(none)")
    out.append("")
    out.append("### Longest sentences")
    for count, sentence, title in longest:
        out.append(f"- {count} words, {title}: \"{snippet(sentence)}\"")
    out.append("")
    out.append("### Paragraphs over 250 words")
    if long_paragraphs:
        for title, count, para in long_paragraphs[:10]:
            out.append(f"- {count} words, {title}: \"{snippet(para)}\"")
        if len(long_paragraphs) > 10:
            out.append(f"- ... and {len(long_paragraphs) - 10} more")
    else:
        out.append("None.")
    print("\n".join(out))


if __name__ == "__main__":
    main()
