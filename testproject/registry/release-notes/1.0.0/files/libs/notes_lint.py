import sys

# Rejects empty release notes; exit 1 fails the run.
text = sys.stdin.read()
if not text.strip():
    print("Release notes are empty.", file=sys.stderr)
    sys.exit(1)
sys.stdout.write(text)
