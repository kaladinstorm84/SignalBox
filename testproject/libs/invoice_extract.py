"""Structured-repair demo: a deliberately sloppy invoice extractor.

The first call prints prose and a JSON object that breaks the invoice schema
(the total is a string and the currency is missing). SignalBox validates it,
then re-runs this script with the validation errors appended to stdin; the
second call sees them and prints a conforming document.
"""
import json
import sys

text = sys.stdin.read()

if "Validation errors" not in text:
    print("Here is what I found in the invoice:")
    print(json.dumps({"invoice": "INV-1042", "supplier": "Northwind Rail Parts", "total": "1,240.00", "lines": []}))
    sys.exit(0)

print(json.dumps({
    "invoice": "INV-1042",
    "supplier": "Northwind Rail Parts",
    "total": 1240.0,
    "currency": "GBP",
    "lines": [
        {"item": "Signal relay", "amount": 900.0},
        {"item": "Cabling", "amount": 340.0},
    ],
}))
