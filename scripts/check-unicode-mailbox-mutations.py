#!/usr/bin/env python3
"""Offline Unicode mailbox mutation checks using Go overlays, never source edits.

Run from any directory: python3 scripts/check-unicode-mailbox-mutations.py
Requires the normal Go test toolchain and SQLite fixture dependencies. Every run
retains baseline/mutant JSONL, stderr, overlays, and summary.json under a printed
temporary directory. A skipped test, changed source anchor, timeout, build error,
or unexpected failure is an error, not a killed mutant.
"""

import argparse
import json
from pathlib import Path
import re
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
NATIVE = "pkg/mail/native_lookup.go"
INDEX = "pkg/mail/envelope_index.go"
LOOKUP = "TestNativeMailboxUnicodeLookup"
AMBIGUITY = "TestNativeMailboxUnicodeAmbiguityPreventsMutation"
SQL = "TestIndexMailboxUnicodeResolutionAndSnapshotParity"
CONNECTED = "TestNativeUnicodeLocatedMessageMutation"


def replace_once(source, anchor, replacement):
    if source.count(anchor) != 1:
        raise RuntimeError(f"expected exactly one source anchor: {anchor!r}")
    return source.replace(anchor, replacement, 1)


def native_without_normalization(source):
    # Remove all five NFC calls in this resolver, retaining raw identity handling.
    for value in ("name", "box.name", "box.path", "candidate.name", "path"):
        source = replace_once(source, f"norm.NFC.String({value})", value)
    return replace_once(source, '\t"golang.org/x/text/unicode/norm"\n', "")


def native_without_ambiguity(source):
    return replace_once(source, "if len(matches) != 1 {", "if len(matches) < 1 {")


def sql_without_literal_priority(source):
    return replace_once(
        source,
        'rank := "case when " + literalExact + " then 0 when " + preference.exact + " then 1 when " + literalBroad + " then 2 else 3 end"',
        'rank := "case when " + preference.exact + " then 0 when " + literalExact + " then 1 when " + literalBroad + " then 2 else 3 end"',
    )


def sql_without_ambiguity(source):
    return replace_once(
        source,
        "case when MailboxRank in (1, 3) and count(*) over (partition by MailboxRank) > 1 then 1 else 0 end as Ambiguous",
        "0 as Ambiguous",
    )


def sql_cases(names):
    return [f"{SQL}/{backend}/{name.replace(' ', '_')}"
            for backend in ("legacy", "native") for name in names]


LOOKUP_CASES = [f"{LOOKUP}/NFD_caller_NFC_live", f"{LOOKUP}/NFC_caller_NFD_live"]
AMBIGUITY_CASES = [f"{AMBIGUITY}/Café", f"{AMBIGUITY}/Cafe\u0301"]
PRIORITY_CASES = sql_cases(["raw NFC exact wins over lower rowid", "raw NFD exact wins over lower rowid"])
SQL_AMBIGUITY_CASES = sql_cases(["normalized exact ambiguity", "normalized broad ambiguity"])

def connected_cases(ambiguous):
    return [f"{CONNECTED}/{spelling}/nested={nested}/ambiguous={ambiguous}"
            for spelling in ("NFD_index_NFC_Mail", "NFC_index_NFD_Mail")
            for nested in ("false", "true")]


MUTANTS = [
    ("native-normalization", NATIVE, native_without_normalization, LOOKUP,
     LOOKUP_CASES, "mailbox not found"),
    ("sql-literal-priority", INDEX, sql_without_literal_priority,
     SQL + "/(legacy|native)/raw_(NFC|NFD)_exact_wins_over_lower_rowid$",
     PRIORITY_CASES, "resolver identity/counts="),
    ("native-ambiguity", NATIVE, native_without_ambiguity, AMBIGUITY,
     AMBIGUITY_CASES, "distinct canonical-equivalent mailboxes accepted:"),
    ("connected-native-normalization", NATIVE, native_without_normalization,
     CONNECTED + "/(NFD_index_NFC_Mail|NFC_index_NFD_Mail)/nested=(false|true)/ambiguous=false$",
     connected_cases("false"), "mailbox not found"),
    ("connected-native-ambiguity", NATIVE, native_without_ambiguity,
     CONNECTED + "/(NFD_index_NFC_Mail|NFC_index_NFD_Mail)/nested=(false|true)/ambiguous=true$",
     connected_cases("true"), "ambiguous located mailbox must reject the mutation:"),
    ("sql-ambiguity", INDEX, sql_without_ambiguity,
     SQL + "/(legacy|native)/normalized_(exact|broad)_ambiguity$",
     SQL_AMBIGUITY_CASES, "ambiguous resolver must fail closed:"),
]


def run_tests(directory, name, selection, required, timeout, overlay=None, marker=None):
    command = ["go", "test", "-json", "-count=1", "-timeout=2m"]
    if overlay:
        command += ["-overlay", str(overlay)]
    command += ["./pkg/mail", "-run", "^" + selection]
    stdout = directory / f"{name}.jsonl"
    stderr = directory / f"{name}.stderr.log"
    (directory / f"{name}.command.json").write_text(json.dumps(command, indent=2) + "\n")
    with stdout.open("w") as out, stderr.open("w") as err:
        result = subprocess.run(command, cwd=ROOT, stdout=out, stderr=err, timeout=timeout)
    events = [json.loads(line) for line in stdout.read_text().splitlines() if line.strip()]
    statuses = {event["Test"]: event["Action"] for event in events
                if "Test" in event and event["Action"] in ("pass", "fail", "skip")}
    if not statuses or any(status == "skip" for status in statuses.values()):
        raise RuntimeError(f"{name}: no completed tests or skipped coverage; see {stdout}")
    expected_status = "fail" if marker else "pass"
    for test in required:
        if statuses.get(test) != expected_status:
            raise RuntimeError(f"{name}: {test} did not {expected_status}; see {stdout}")
        if marker:
            output = "".join(event.get("Output", "") for event in events if event.get("Test") == test)
            if marker not in output:
                raise RuntimeError(f"{name}: missing expected assertion in {test}; see {stdout}")
    failures = {test for test, status in statuses.items() if status == "fail"}
    allowed = {test for leaf in required for test in statuses
               if leaf == test or leaf.startswith(test + "/")}
    if marker:
        if result.returncode != 1 or failures - allowed:
            raise RuntimeError(f"{name}: unexpected exit/failures {result.returncode}, {sorted(failures - allowed)}")
        output = stdout.read_text() + stderr.read_text()
        if re.search(r"panic:|build failed|setup failed|fatal error:|test timed out", output, re.I):
            raise RuntimeError(f"{name}: infrastructure/runtime failure cannot kill a mutant")
    elif result.returncode != 0 or failures:
        raise RuntimeError(f"{name}: healthy baseline failed; see {stdout}")
    packages = [event for event in events if "Test" not in event and event["Action"] in ("pass", "fail")]
    if len(packages) != 1 or packages[0]["Action"] != expected_status:
        raise RuntimeError(f"{name}: missing expected package completion; see {stdout}")
    return {"name": name, "status": "killed" if marker else "passed", "assertions": required,
            "stdout": str(stdout), "stderr": str(stderr)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--timeout", type=int, default=180, help="wall-clock seconds per Go invocation (default: 180)")
    args = parser.parse_args()
    if args.timeout <= 0:
        parser.error("--timeout must be positive")
    directory = Path(tempfile.mkdtemp(prefix="mail-unicode-mutations-")).resolve()
    print(f"Evidence: {directory}", flush=True)
    summary = []
    try:
        # Prepare every independent overlay first; stale anchors fail before testing.
        overlays = {}
        for name, relative, mutate, *_ in MUTANTS:
            source_path = ROOT / relative
            mutated = directory / f"{name}.go"
            mutated.write_text(mutate(source_path.read_text()))
            overlay = directory / f"{name}.overlay.json"
            overlay.write_text(json.dumps({"Replace": {str(source_path): str(mutated)}}, indent=2) + "\n")
            overlays[name] = overlay
        required = (LOOKUP_CASES + AMBIGUITY_CASES + PRIORITY_CASES + SQL_AMBIGUITY_CASES
                    + connected_cases("false") + connected_cases("true"))
        selection = f"({LOOKUP}|{AMBIGUITY}|{SQL}|{CONNECTED})$"
        summary.append(run_tests(directory, "baseline", selection, required, args.timeout))
        print("baseline | passed", flush=True)
        for name, _, _, selection, required, marker in MUTANTS:
            summary.append(run_tests(directory, name, selection, required, args.timeout, overlays[name], marker))
            print(f"{name} | killed | {len(required)} expected failing assertions", flush=True)
    except (OSError, ValueError, RuntimeError, subprocess.TimeoutExpired) as error:
        summary.append({"status": "error", "error": str(error)})
        print(f"ERROR: {error}", file=sys.stderr)
        return 1
    finally:
        (directory / "summary.json").write_text(json.dumps(summary, indent=2, ensure_ascii=False) + "\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
