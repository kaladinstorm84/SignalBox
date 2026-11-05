import sys


def main():
    payload = sys.stdin.read()
    if not payload.strip():
        print("Verification failed: the planner returned empty input.", file=sys.stderr)
        return 1
    sys.stdout.write(payload)
    return 0


if __name__ == "__main__":
    sys.exit(main())