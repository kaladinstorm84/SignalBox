"""Upload demo: summarises the files attached to an execution.

Nodes with files: path get read-only copies of the uploads in the directory
named by SIGNALBOX_FILES_DIR. This script reports each file's size, line
count and, for CSVs, the row count and the total of any "amount" column.
"""
import csv
import json
import os
import sys

files_dir = os.environ.get("SIGNALBOX_FILES_DIR", "")
if not files_dir:
    print("No files were attached (SIGNALBOX_FILES_DIR is not set).")
    sys.exit(0)

report = []
for name in sorted(os.listdir(files_dir)):
    path = os.path.join(files_dir, name)
    entry = {"file": name, "bytes": os.path.getsize(path)}
    try:
        with open(path, encoding="utf-8") as fh:
            text = fh.read()
    except UnicodeDecodeError:
        entry["kind"] = "binary"
        report.append(entry)
        continue
    entry["lines"] = len(text.splitlines())
    if name.lower().endswith(".csv"):
        rows = list(csv.DictReader(text.splitlines()))
        entry["rows"] = len(rows)
        amounts = [float(r["amount"]) for r in rows if r.get("amount")]
        if amounts:
            entry["amount_total"] = round(sum(amounts), 2)
    report.append(entry)

print(json.dumps({"files": report}, indent=2))
