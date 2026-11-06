#!/usr/bin/env python3
"""Run Playwright against a fresh local SQLite DB, never operator resources."""
import os
from pathlib import Path
import socket
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]


def main():
    # Only a fresh directory owned by this process is migrated and removed.
    preferred = Path("/tmp/opencode")
    parent = str(preferred) if preferred.is_dir() else None
    with tempfile.TemporaryDirectory(prefix="share-secrets-e2e-", dir=parent) as directory:
        database = str(Path(directory) / "secrets.db")
        env = {key: os.environ[key] for key in ("HOME", "PATH", "TMPDIR", "GOCACHE", "GOMODCACHE", "PLAYWRIGHT_BROWSERS_PATH") if key in os.environ}
        env.update({"STORAGE_BACKEND": "sqlite", "SQLITE_PATH": database,
                    "SQLITE_NO_PAYLOAD_BACKUP_ACK": "yes", "CI": "1"})
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            env["PORT"] = str(sock.getsockname()[1])
        subprocess.run(["mise", "exec", "--", "go", "tool", "goose", "-env", "none",
                        "-dir", "migrations/sqlite", "sqlite3", database, "up"],
                       cwd=ROOT, env=env, check=True)
        subprocess.run(["mise", "exec", "--", "npm", "run", "test:e2e"],
                       cwd=ROOT, env=env, check=True)


if __name__ == "__main__":
    main()
