import os
import shutil
import subprocess
import sys
from pathlib import Path


def main() -> None:
    repo_root = Path(__file__).resolve().parent.parent
    go = shutil.which("go")
    if go is None:
        print("go binary not found in PATH", file=sys.stderr)
        raise SystemExit(1)

    cmd = [go, "run", "./cmd/mcp"]
    proc = subprocess.Popen(cmd, cwd=repo_root, env=os.environ.copy())
    raise SystemExit(proc.wait())
