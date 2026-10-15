#!/usr/bin/env python3
"""Compare read-only CLI workloads without retaining message output in the report."""
import argparse
import hashlib
import json
import os
import platform
import random
import statistics
import subprocess
import tempfile
import time
from pathlib import Path

READ_COMMANDS = {("accounts", "list"), ("mailboxes", "list"), ("messages", "list"),
                 ("messages", "read"), ("messages", "scan"), ("drafts", "list"),
                 ("threads", "list"), ("show",), ("search",), ("inbox",), ("unread",)}


def summary(rows):
    ordered = sorted(row["wallMs"] for row in rows)
    return {"samples": len(rows), "p50Ms": statistics.median(ordered),
            "p95Ms": ordered[max(0, (95 * len(ordered) + 99) // 100 - 1)],
            "failures": sum(row["exitCode"] != 0 or not row["envelopeOK"] for row in rows),
            "p50CpuMs": statistics.median(row["cpuMs"] for row in rows),
            "p95PeakBytes": sorted(row["peakBytes"] for row in rows)[max(0, (95 * len(rows) + 99) // 100 - 1)]}


def run(binary, args, backend=None):
    command = [str(binary)] + (["--backend", backend] if backend else []) + args
    with tempfile.TemporaryDirectory(prefix="mail-native-measure-") as directory:
        metrics = Path(directory) / "metrics.json"
        env = dict(os.environ, MAIL_APP_CLI_METRICS_FILE=str(metrics))
        env.pop("MAIL_APP_CLI_BACKEND", None)
        with tempfile.TemporaryFile() as stdout, tempfile.TemporaryFile() as stderr:
            started = time.monotonic()
            process = subprocess.Popen(command, stdin=subprocess.DEVNULL, stdout=stdout, stderr=stderr, env=env)
            _, status, usage = os.wait4(process.pid, 0)
            process.returncode = os.waitstatus_to_exitcode(status)
            elapsed = (time.monotonic() - started) * 1000
            stdout.seek(0)
            # Verify the CLI envelope, then discard all message/account data.
            try:
                result = json.load(stdout)
                envelope_ok = isinstance(result, dict) and result.get("ok") is True
                count = result.get("meta", {}).get("count")
            except (ValueError, TypeError):
                envelope_ok, count = False, None
        row = {"wallMs": elapsed, "cpuMs": (usage.ru_utime + usage.ru_stime) * 1000,
               "peakBytes": usage.ru_maxrss * (1 if platform.system() == "Darwin" else 1024),
               "exitCode": process.returncode, "envelopeOK": envelope_ok, "count": count}
        if metrics.exists():
            row["metrics"] = json.loads(metrics.read_text())
        return row


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", type=Path, required=True)
    parser.add_argument("--candidate", type=Path, required=True)
    parser.add_argument("--manifest", type=Path, required=True, help="Private JSON list of {name,args} read-only workloads")
    parser.add_argument("--fresh", type=int, default=50)
    parser.add_argument("--warm", type=int, default=100)
    parser.add_argument("--out", type=Path, required=True)
    options = parser.parse_args()
    for binary in (options.baseline, options.candidate):
        if not binary.is_absolute() or not binary.is_file() or not os.access(binary, os.X_OK):
            parser.error("binaries must be existing executable absolute paths")
    if min(options.fresh, options.warm) < 1:
        parser.error("sample counts must be positive")
    workloads = json.loads(options.manifest.read_text())
    for workload in workloads:
        args = workload.get("args", [])
        if not isinstance(args, list) or not all(isinstance(arg, str) for arg in args):
            parser.error("workload args must be a list of strings")
        if not any(tuple(args[:len(prefix)]) == prefix for prefix in READ_COMMANDS):
            parser.error("manifest contains a command outside the read-only allowlist")
        if "--json" not in args:
            parser.error("every workload must request --json")
        if any(arg in ("--help", "-h", "--quiet", "-q", "--count", "--ids-only", "--jq") or arg.startswith("--jq=") for arg in args):
            parser.error("workloads must preserve the complete JSON envelope")
    report = {"method": "Randomized paired fresh CLI processes; warm series follows one unmeasured warmup per backend. Mail stays running. No message output retained.",
              "baselineSHA256": hashlib.sha256(options.baseline.read_bytes()).hexdigest(),
              "candidateSHA256": hashlib.sha256(options.candidate.read_bytes()).hexdigest(),
              "platform": platform.platform(), "seed": 30000, "workloads": {}}
    rng = random.Random(30000)
    modes = [("baseline", options.baseline, None), ("native", options.candidate, "native"), ("legacy", options.candidate, "legacy")]
    for workload in workloads:
        measurements = {}
        for series, samples in (("fresh", options.fresh), ("warm", options.warm)):
            grouped = {name: [] for name, _, _ in modes}
            if series == "warm":
                for _, binary, backend in modes:
                    run(binary, workload["args"], backend)
            for _ in range(samples):
                order = modes.copy()
                rng.shuffle(order)
                for name, binary, backend in order:
                    grouped[name].append(run(binary, workload["args"], backend))
            measurements[series] = {name: {"summary": summary(rows), "rows": rows} for name, rows in grouped.items()}
            print(workload["name"], series, {name: summary(rows) for name, rows in grouped.items()}, flush=True)
        report["workloads"][workload["name"]] = measurements
        options.out.parent.mkdir(parents=True, exist_ok=True)
        with options.out.open("w") as target:
            json.dump(report, target, indent=2)
        options.out.chmod(0o600)


if __name__ == "__main__":
    main()
