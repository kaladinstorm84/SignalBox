"""Checkpoint for the recovery-drill demo pipeline.

Fails until the flag file recovery-drill.fixed exists in the project root
(code agents run with the project root as their working directory).
"""
import os
import sys

FLAG = "recovery-drill.fixed"


def main():
    payload = sys.stdin.read()
    if not os.path.exists(FLAG):
        print(
            f"Checkpoint failed: {FLAG} not found. Create it, then restart "
            "the verify stage to reuse all earlier work.",
            file=sys.stderr,
        )
        return 1
    sys.stdout.write(payload)
    return 0


if __name__ == "__main__":
    sys.exit(main())
