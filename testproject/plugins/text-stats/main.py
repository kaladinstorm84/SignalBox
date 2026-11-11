import json
import sys

from text_tools import summarize


print(json.dumps(summarize(sys.stdin.read()), ensure_ascii=False))
