"""Read cargo's build metadata; never build/run a binary or inspect credentials."""
import json
from pathlib import Path
import sys


def select(lines):
    matches = []
    for line in lines:
        record = json.loads(line)
        if (record.get("reason") == "compiler-artifact" and record.get("target", {}).get("name") == "employee_config_core"
                and record.get("profile", {}).get("test") and record.get("executable")):
            matches.append(record["executable"])
    if len(matches) != 1:
        raise ValueError("fixture-binary-count")
    return matches[0]


if __name__ == "__main__":
    print(select(Path(sys.argv[1]).read_text().splitlines()))
