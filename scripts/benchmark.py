#!/usr/bin/env python3
"""Owned local-server campaign. Standard library only; never targets a VPS."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import secrets
import socket
import statistics
import subprocess
import threading
import tempfile
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
TICKS = os.sysconf("SC_CLK_TCK")


def command(args, **kwargs):
    return subprocess.check_output(args, cwd=ROOT, text=True, **kwargs).strip()


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def process_sample(pid):
    try:
        # Fields after the final ')' start at Linux proc stat field 3.
        fields = Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[1].split()
        return {"cpuSeconds": (int(fields[11]) + int(fields[12])) / TICKS,
                "rssBytes": int(fields[21]) * os.sysconf("SC_PAGE_SIZE")}
    except (FileNotFoundError, ProcessLookupError):
        return None


def cgroup_path(pid):
    for line in Path(f"/proc/{pid}/cgroup").read_text().splitlines():
        if line.startswith("0::"):
            path = Path("/sys/fs/cgroup") / line[3:].lstrip("/")
            if (path / "cpu.stat").exists():
                return path
    raise RuntimeError("Cannot observe target cgroup; refusing to claim enforced limits")


def counters(path):
    result = {}
    for filename in ("cpu.stat", "memory.events"):
        for line in (path / filename).read_text().splitlines():
            key, value = line.split()
            result[f"{filename}:{key}"] = int(value)
    result["memory.current"] = int((path / "memory.current").read_text())
    return result


class Monitor:
    def __init__(self, server_pid, generator_pid, group):
        self.pids = {"server": server_pid, "generator": generator_pid}
        self.group = group
        self.samples = []
        self.stop_event = threading.Event()
        self.thread = threading.Thread(target=self.collect, daemon=True)

    def collect(self):
        start = time.monotonic()
        while not self.stop_event.is_set():
            sample = {"seconds": time.monotonic() - start, "unixNs": time.time_ns()}
            for name, pid in self.pids.items():
                sample[name] = process_sample(pid)
            if self.group:
                sample["cgroup"] = counters(self.group)
            self.samples.append(sample)
            self.stop_event.wait(.2)

    def __enter__(self):
        self.thread.start()
        return self

    def __exit__(self, *_):
        self.stop_event.set()
        self.thread.join()


class Server:
    def __init__(self, args, binary, token, policy):
        self.args, self.binary, self.token, self.policy = args, binary, token, policy
        self.process = None
        self.container = None
        self.port = free_port()
        self.group = None
        self.db_dir = None

    def __enter__(self):
        # Allowlisted environment only: never inherit production app settings.
        env = {"PATH": os.environ["PATH"], "METRICS_TOKEN": self.token,
               "SECRET_TTL": "1m", "ACTIVE_SECRET_LIMIT": "500",
               "CREATE_RATE_PER_MINUTE": "60" if self.policy else "100000",
               "CREATE_RATE_BURST": "20" if self.policy else "100000"}
        try:
            if self.args.backend == "sqlite":
                self.db_dir = tempfile.TemporaryDirectory(prefix="bench-db-", dir=self.args.output)
                db_path = Path(self.db_dir.name) / "secrets.db"
                subprocess.run(["mise", "exec", "--", "go", "tool", "goose", "-env", "none",
                                "-dir", "migrations/sqlite", "sqlite3", str(db_path), "up"],
                               cwd=ROOT, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                env["STORAGE_BACKEND"] = "sqlite"
                env["SQLITE_NO_PAYLOAD_BACKUP_ACK"] = "yes"
                env["SQLITE_PATH"] = "/bench-data/secrets.db" if self.args.mode == "limited" else str(db_path)
                if self.args.mode == "limited":
                    env["SQLITE_TMPDIR"] = "/tmp"
            if self.args.mode == "limited":
                self.container = "share-secrets-bench-" + secrets.token_hex(6)
                docker = ["docker", "--context", self.args.context]
                run = docker + ["run", "--detach", "--pull=never", "--name", self.container,
                                "--cpus", "0.5", "--memory", "128m", "--memory-swap", "128m",
                                "--pids-limit", "64", "--read-only", "--cap-drop", "ALL",
                                "--security-opt", "no-new-privileges", "--user", "0:0" if self.db_dir else "65532:65532",
                                "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=16m,mode=1777",
                                "--publish", f"127.0.0.1:{self.port}:8080",
                                "--mount", f"type=bind,src={self.binary},dst=/bench-server,readonly"]
                if self.db_dir:
                    run += ["--mount", f"type=bind,src={self.db_dir.name},dst=/bench-data"]
                for key in env:
                    if key != "PATH":
                        run += ["--env", key]
                run += ["--env", "ADDR=:8080", "debian:bookworm-slim", "/bench-server"]
                subprocess.run(run, env={**os.environ, **env}, cwd=ROOT, check=True,
                               stdout=subprocess.DEVNULL)
                self.pid = int(command(docker + ["inspect", "--format", "{{.State.Pid}}", self.container]))
                self.group = cgroup_path(self.pid)
                memory_max = (self.group / "memory.max").read_text().strip()
                cpu_max = (self.group / "cpu.max").read_text().strip()
                quota, period = cpu_max.split()
                if memory_max != str(128 * 1024 * 1024) or quota == "max" or int(quota) / int(period) != .5:
                    raise RuntimeError("Requested CPU/memory limits are not enforced")
            else:
                env["ADDR"] = f"127.0.0.1:{self.port}"
                self.process = subprocess.Popen([str(self.binary)], env=env, cwd=ROOT,
                                                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                self.pid = self.process.pid
            self.url = f"http://127.0.0.1:{self.port}"
            for _ in range(100):
                try:
                    with urllib.request.urlopen(self.url + "/healthz", timeout=.5) as response:
                        if response.status == 204:
                            return self
                except OSError:
                    pass
                time.sleep(.1)
            raise RuntimeError("Owned benchmark server failed readiness")
        except BaseException:
            self.__exit__()
            raise

    def __exit__(self, *_):
        if self.container:
            # Only the exact generated container owned by this invocation.
            subprocess.run(["docker", "--context", self.args.context, "rm", "--force", self.container],
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
            self.container = None
        if self.process:
            self.process.terminate()
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait()
        if self.db_dir:
            self.db_dir.cleanup()
            self.db_dir = None


def execute_case(args, generator, server, scenario, rate, duration, seed, output, terminal=0):
    env = {**os.environ, "BENCH_METRICS_TOKEN": server.token}
    argv = [str(generator), "-target", server.url, "-scenario", scenario, "-rate", str(rate),
            "-duration", f"{duration}s", "-seed", str(seed), "-terminal", str(terminal)]
    with output.open("w") as stdout:
        process = subprocess.Popen(argv, cwd=ROOT, env=env, stdout=stdout, stderr=subprocess.PIPE, text=True)
        try:
            with Monitor(server.pid, process.pid, server.group) as monitor:
                setup_seconds = max(300, terminal * .3 + 120)
                _, error = process.communicate(timeout=duration + setup_seconds + 100)
        except BaseException:
            process.kill()
            process.wait()
            raise
    if process.returncode not in (0, 2):
        raise RuntimeError(f"Generator setup failed (no payload diagnostics): {error[:200]}")
    result = json.loads(output.read_text())
    result["resources"] = monitor.samples
    result["mode"] = args.mode
    result["ratePolicy"] = "production-default" if server.policy else "raised-for-isolation"
    result["generatorExitCode"] = process.returncode
    output.write_text(json.dumps(result, indent=2) + "\n")
    return result


def resource_summary(result):
    summary = {}
    samples = [sample for sample in result["resources"]
               if result.get("windowStartUnixNs", 0) <= sample["unixNs"] <= result.get("windowEndUnixNs", 0)]
    for name in ("server", "generator"):
        points = [s[name] for s in samples if s[name] is not None]
        summary[name + "PeakRSSMiB"] = max((p["rssBytes"] for p in points), default=0) / 1024**2
        elapsed = samples[-1]["seconds"] - samples[0]["seconds"] if len(samples) > 1 else 0
        summary[name + "CPUcores"] = ((points[-1]["cpuSeconds"] - points[0]["cpuSeconds"]) / elapsed
                                      if elapsed and len(points) > 1 else 0)
    if samples and "cgroup" in samples[0]:
        first, last = samples[0]["cgroup"], samples[-1]["cgroup"]
        summary["cgroupPeakSampledMiB"] = max(s["cgroup"]["memory.current"] for s in samples) / 1024**2
        summary["throttledSeconds"] = (last["cpu.stat:throttled_usec"] - first["cpu.stat:throttled_usec"]) / 1e6
        summary["oomKills"] = last["memory.events:oom_kill"] - first["memory.events:oom_kill"]
    return summary


def retained_window_valid(result):
    return not result["failures"] and result["offered"] > 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mode", choices=("limited", "native"), default="limited")
    parser.add_argument("--backend", choices=("memory", "sqlite"), default="memory")
    parser.add_argument("--context", default="rootless")
    parser.add_argument("--repeats", type=int, default=3)
    parser.add_argument("--step-seconds", type=int, default=10)
    parser.add_argument("--soak-seconds", type=int, default=120)
    parser.add_argument("--rates", default="10,25,50,100,200,400")
    parser.add_argument("--skip-expiry", action="store_true", help="explicit shorter campaign; report remains partial")
    parser.add_argument("--retained", type=int, default=9500, help="HTTP-seeded terminal records; 0 explicitly skips")
    parser.add_argument("--only", default="", help="comma-separated case labels; explicitly partial campaign")
    parser.add_argument("--retained-rates", default="25,50,100,200", help="successive arrival windows on the same retained store")
    args = parser.parse_args()
    rates = [int(value) for value in args.rates.split(",")]
    retained_rates = [int(value) for value in args.retained_rates.split(",")]
    if not (1 <= args.repeats <= 5 and 1 <= args.step_seconds <= 60 and 1 <= args.soak_seconds <= 600
            and rates and len(rates) <= 10 and all(1 <= rate <= 2000 for rate in rates)
            and 0 <= args.retained <= 9500 and 1 <= len(retained_rates) <= 8
            and all(1 <= rate <= 2000 for rate in retained_rates)):
        parser.error("Campaign parameters exceed bounded local budget")
    if args.mode == "limited":
        security = command(["docker", "--context", args.context, "info", "--format", "{{json .SecurityOptions}}"])
        if "name=rootless" not in security:
            raise RuntimeError("Limited mode requires a verified rootless daemon")
    output = ROOT / "benchmark-results" / (time.strftime("%Y%m%dT%H%M%S") + "-" + secrets.token_hex(3))
    output.mkdir(parents=True)
    args.output = output
    server, generator = output / "server", output / "generator"
    build_env = {**os.environ, "CGO_ENABLED": "0"}
    for package, binary in ((".", server), ("./cmd/benchmark", generator)):
        subprocess.run(["mise", "exec", "--", "go", "build", "-trimpath", "-ldflags=-s -w", "-o", str(binary), package],
                       cwd=ROOT, env=build_env, check=True)
    metadata = {"schemaVersion": 1, "platform": platform.platform(), "cpuCount": os.cpu_count(),
                "go": command(["mise", "exec", "--", "go", "version"]),
                "baseCommit": command(["git", "rev-parse", "HEAD"]),
                "workingTreeDirty": bool(command(["git", "status", "--porcelain"])),
                "serverSHA256": hashlib.sha256(server.read_bytes()).hexdigest(),
                "generatorSHA256": hashlib.sha256(generator.read_bytes()).hexdigest(),
                "config": {key: value for key, value in vars(args).items() if key != "output"}, "sampleIntervalSeconds": .2,
                "build": "CGO_ENABLED=0 go build -trimpath -ldflags=-s -w",
                "payloadMix": {"1024": 90, "16384": 9, "45056": 1},
                "diagnosticCriteria": {"completedOfferedMin": .99, "cycleP99MsMax": 1000},
                "exclusions": ["VPS", "TLS/proxy", "browser JS", "PostgreSQL", "physical erasure", "internal Go GC metrics"] + (["SQLite"] if args.backend == "memory" else [])}
    (output / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
    rows = []
    cases = [("smoke", "mixed", 5, 3, 0, False)]
    cases += [(f"step-{rate}", "mixed", rate, args.step_seconds, 0, False) for rate in rates]
    cases += [("occupied", "mixed", min(rates), args.step_seconds, 450, False),
              ("burst", "burst", max(rates), 10, 0, False),
              ("soak", "mixed", min(rates), args.soak_seconds, 0, False),
              ("policy", "policy", 20, 5, 0, True),
              ("capacity", "capacity", 1, 1, 500, False),
              ("concurrency", "concurrency", 1, 1, 0, False)]
    if not args.skip_expiry:
        cases += [("expiry", "expiry", 1, 62, 0, False)]
    if args.retained:
        cases += [("retained", "mixed", 25, args.step_seconds, 0, False)]
    if args.only:
        selected = set(args.only.split(","))
        if not selected.issubset({case[0] for case in cases}):
            parser.error("Unknown selected case")
        cases = [case for case in cases if case[0] in selected]
    for label, scenario, rate, duration, seed, policy in cases:
        for repeat in range(1, args.repeats + 1):
            print(f"{label} repetition {repeat}/{args.repeats}", flush=True)
            token = secrets.token_urlsafe(32)
            with Server(args, server.resolve(), token, policy) as owned:
                # Warmup is excluded from timings, but retained metadata is
                # intentionally not reset until a fresh server for the next case.
                if scenario in ("mixed", "burst", "policy") and not policy:
                    execute_case(args, generator, owned, "mixed", 5, 2, 0,
                                 output / f"{label}-{repeat}-warmup.json")
                result = execute_case(args, generator, owned, scenario, rate, duration, seed,
                                      output / f"{label}-{repeat}.json", args.retained if label == "retained" else 0)
                rows.append({"case": label, "repetition": repeat,
                             "offered": result["offered"], "completed": result["completed"],
                             "rejected": result["rejected"], "dropped": result["dropped"],
                             "failures": result["failures"], "latency": result["cycleLatency"],
                             "healthy": result["diagnosticHealthy"],
                             "resources": resource_summary(result)})
                if label == "retained":
                    for next_rate in retained_rates:
                        if not retained_window_valid(result):
                            print("retained preparation/window failed; subsequent rates skipped", flush=True)
                            break
                        if next_rate == 25:
                            continue
                        next_label = f"retained-{next_rate}"
                        result = execute_case(args, generator, owned, "mixed", next_rate,
                                              args.step_seconds, 0, output / f"{next_label}-{repeat}.json")
                        rows.append({"case": next_label, "repetition": repeat,
                                     "offered": result["offered"], "completed": result["completed"],
                                     "rejected": result["rejected"], "dropped": result["dropped"],
                                     "failures": result["failures"], "latency": result["cycleLatency"],
                                     "healthy": result["diagnosticHealthy"],
                                     "resources": resource_summary(result)})
                # Write after every case so interruption retains completed evidence.
                (output / "summary.json").write_text(json.dumps(rows, indent=2) + "\n")
    lines = ["# Local HTTP benchmark campaign", "", f"Mode: {args.mode}; repeats: {args.repeats}.",
             "Diagnostic criteria, not a product SLO. See metadata.json for limits/exclusions.", "",
             "| Case | Healthy runs | Completed/offered (sum) | Median run p99 ms | Peak sampled server RSS MiB |",
             "| --- | ---: | ---: | ---: | ---: |"]
    for label in dict.fromkeys(row["case"] for row in rows):
        group = [row for row in rows if row["case"] == label]
        lines.append(f"| {label} | {sum(row['healthy'] for row in group)}/{len(group)} | "
                     f"{sum(row['completed'] for row in group)}/{sum(row['offered'] for row in group)} | "
                     f"{statistics.median(row['latency']['p99Ms'] for row in group):.2f} | "
                     f"{max(row['resources']['serverPeakRSSMiB'] for row in group):.2f} |")
    (output / "report.md").write_text("\n".join(lines) + "\n")
    # Remove only generated executables, keeping reports and samples.
    server.unlink()
    generator.unlink()
    print(f"Evidence retained at {output.relative_to(ROOT)}", flush=True)


if __name__ == "__main__":
    main()
