#!/usr/bin/env python3
"""Opt-in, serial archive/sync acceptance check for one disposable fixture."""

import argparse
import datetime as dt
from email.utils import parseaddr
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import time
import uuid


PREFIX = "mail-app-cli-archive-fixture-"


class CheckFailed(Exception):
    pass


def require(condition, message):
    if not condition:
        raise CheckFailed(message)


def timestamp():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def identity(message):
    date = message.get("dateSent", "").strip()
    try:
        date = dt.datetime.fromisoformat(date.replace("Z", "+00:00"))
        require(date.tzinfo is not None, "sent date has no timezone")
        date = date.astimezone(dt.timezone.utc).isoformat()
    except ValueError as exc:
        raise CheckFailed("fixture has no valid sent-date identity") from exc
    result = {"subject": message.get("subject", "").strip(),
              "sender": message.get("sender", "").strip(),
              "dateSent": date, "messageSize": message.get("messageSize")}
    require(result["subject"] and result["sender"] and
            type(result["messageSize"]) is int and result["messageSize"] > 0,
            "fixture requires full sender/subject/sent-date/size identity")
    return result


class Check:
    def __init__(self, args, evidence):
        self.args = args
        self.evidence = evidence
        self.deadline = time.monotonic() + args.budget_seconds
        self.step = 0
        self.state = {"startedAt": timestamp(), "status": "running",
                      "account": args.account, "source": "INBOX",
                      "senderAccount": args.sender_account or args.account,
                      "destination": args.destination, "marker": args.marker,
                      "initialLocalId": args.message_id, "cli": args.cli,
                      "apply": args.apply, "archiveAttempted": False}
        self.save()

    def save(self):
        (self.evidence / "result.json").write_text(
            json.dumps(self.state, indent=2) + "\n", encoding="utf-8")

    def run(self, label, arguments):
        remaining = self.deadline - time.monotonic()
        require(remaining > 0, "overall observation budget exhausted")
        self.step += 1
        stem = self.evidence / f"{self.step:03d}-{label}"
        command = [self.args.cli, *arguments, "--json"]
        record = {"command": command, "startedAt": timestamp(),
                  "timeoutSeconds": min(self.args.command_timeout, remaining)}
        # Each command completes before another Mail operation is started.
        with stem.with_suffix(".stdout").open("wb") as stdout, \
                stem.with_suffix(".stderr").open("wb") as stderr:
            process = None
            try:
                process = subprocess.Popen(command, stdout=stdout, stderr=stderr,
                                           start_new_session=True)
                record["exitCode"] = process.wait(timeout=record["timeoutSeconds"])
            except subprocess.TimeoutExpired:
                record["exitCode"] = None
                record["timedOut"] = True
            except (OSError, KeyboardInterrupt) as exc:
                record["exitCode"] = None
                record["interrupted"] = isinstance(exc, KeyboardInterrupt)
                record["error"] = str(exc)
            finally:
                if process is not None and record.get("exitCode") is None:
                    # Bridges use separate process groups. Give the CLI's Go
                    # cancellation handler time to stop them before escalation.
                    record["shutdownMode"] = "graceful"
                    try:
                        os.killpg(process.pid, signal.SIGTERM)
                    except ProcessLookupError:
                        pass
                    except OSError as exc:
                        record["processGroupStopError"] = str(exc)
                    try:
                        record["stoppedExitCode"] = process.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        record["shutdownMode"] = "hard-stop-cleanup-unconfirmed"
                        record["bridgeCleanupUnconfirmed"] = True
                        try:
                            os.killpg(process.pid, signal.SIGKILL)
                        except ProcessLookupError:
                            pass
                        except OSError as exc:
                            record["processGroupHardStopError"] = str(exc)
                        try:
                            record["stoppedExitCode"] = process.wait(timeout=2)
                        except subprocess.TimeoutExpired:
                            record["processReapTimedOut"] = True
                        except OSError as exc:
                            record["processReapError"] = str(exc)
                    except OSError as exc:
                        record["shutdownMode"] = "cleanup-unconfirmed"
                        record["bridgeCleanupUnconfirmed"] = True
                        record["processReapError"] = str(exc)
                record["endedAt"] = timestamp()
                stem.with_suffix(".step.json").write_text(
                    json.dumps(record, indent=2) + "\n", encoding="utf-8")
        require(record["exitCode"] == 0,
                f"{label} failed or timed out; inspect its private step logs; never replay an uncertain mutation")
        try:
            envelope = json.loads(stem.with_suffix(".stdout").read_text(encoding="utf-8"))
        except (ValueError, UnicodeError) as exc:
            raise CheckFailed(f"{label} did not return valid JSON") from exc
        require(isinstance(envelope, dict) and envelope.get("ok") is True and
                "data" in envelope, f"{label} returned an unsuccessful envelope")
        return envelope["data"]

    def scan(self, label, expected=None, archived=False):
        data = self.run(label, ["messages", "scan", "INBOX", self.args.destination,
                               "-a", self.args.account, "--query", self.args.marker,
                               "--limit", "0"])
        require(data.get("complete") is True and not data.get("nextCursor"),
                "scan coverage is incomplete")
        coverage = data.get("coverage", [])
        require(len(coverage) == 2 and
                {(c.get("account"), c.get("mailbox")) for c in coverage} ==
                {(self.args.account, "INBOX"), (self.args.account, self.args.destination)},
                "scan did not cover the exact selected account and both mailboxes")
        require(all(c.get("exhausted") is True and not c.get("truncated") and
                    c.get("remaining") == 0 and not c.get("error") and not c.get("errors")
                    for c in coverage), "scan has failed or truncated coverage")
        messages = data.get("messages", [])
        require(isinstance(messages, list), "scan messages is not a list")
        members = {"INBOX": [], self.args.destination: []}
        for message in messages:
            require(message.get("account") == self.args.account and
                    message.get("subject") == self.args.marker and
                    re.fullmatch(r"[1-9][0-9]*", message.get("id", "")) and
                    message.get("deleted") is False,
                    "unexpected, deleted, or ambiguous fixture match")
            if expected is not None:
                require(identity(message) == expected, "marker matched a different stable identity")
            boxes = message.get("mailboxes", [])
            require(boxes and len(set(boxes)) == len(boxes) and
                    all(box in members for box in boxes), "unexpected scan mailbox membership")
            for box in boxes:
                members[box].append(message)
        require(all(len(rows) <= 1 for rows in members.values()),
                "fixture identity is ambiguous: multiple local copies in one mailbox")
        if archived:
            require(not members["INBOX"], "fixture recurred or remained in INBOX")
            require(len(members[self.args.destination]) == 1,
                    "fixture identity disappeared from the destination")
            require(members[self.args.destination][0].get("read") is True,
                    "destination fixture is not read")
        else:
            require(len(members["INBOX"]) == 1 and
                    members["INBOX"][0]["id"] == self.args.message_id,
                    "positive INBOX baseline does not match the supplied disposable ID")
        return members

    def receipt(self, data, expected, dry_run=False):
        require(data.get("action") == "archive" and data.get("dryRun") is dry_run and
                data.get("matched") == 1 and len(data.get("items", [])) == 1,
                "archive receipt is not for exactly one fixture")
        item = data["items"][0]
        require(item.get("id") == self.args.message_id and
                item.get("account") == self.args.account and item.get("sourceMailbox") == "INBOX" and
                item.get("subject") == self.args.marker,
                "archive selected an unexpected fixture or scope")
        require(identity(item) == expected, "archive receipt stable identity differs from baseline")
        if dry_run:
            require(item.get("status") == "dry-run" and data.get("attempted") == 0,
                    "preview reported an attempted mutation")
            require(item.get("targetMailbox") == self.args.destination,
                    "preview archive destination is missing or differs")
            return
        require(item.get("targetMailbox") == self.args.destination and
                item.get("status") == "succeeded" and data.get("attempted") == 1 and
                data.get("succeeded") == 1 and data.get("failed") == 0 and
                data.get("skipped") == 0 and data.get("unverified", 0) == 0,
                "archive is skipped, failed, or unverified")
        require(identity(item.get("identity", {})) == expected,
                "receipt lacks the exact captured stable identity")
        observed = item.get("observed", {})
        require(observed.get("account") == self.args.account and
                observed.get("sourceMailbox") == "INBOX" and
                observed.get("destinationMailbox") == self.args.destination and
                observed.get("sourceRemovalRequired") is True and
                observed.get("sourcePresent") is False and
                observed.get("destinationPresent") is True and
                observed.get("destinationRead") is True,
                "receipt did not prove source absence and destination read/presence")

    def execute(self):
        self.run("version", ["version"])
        account = self.run("account", ["accounts", "show", self.args.account])
        require(account.get("name") == self.args.account and account.get("enabled") is True,
                "selected account is absent, disabled, or ambiguous")
        sender_name = self.args.sender_account or self.args.account
        sender_account = account
        if sender_name != self.args.account:
            sender_account = self.run("sender-account", ["accounts", "show", sender_name])
        require(sender_account.get("name") == sender_name and sender_account.get("enabled") is True,
                "selected sender account is absent, disabled, or ambiguous")
        boxes = self.run("mailboxes", ["mailboxes", "list", "-a", self.args.account, "--no-cache"])
        for name in ("INBOX", self.args.destination):
            require(sum(box.get("account") == self.args.account and box.get("name") == name
                        for box in boxes) == 1, f"mailbox {name!r} is absent or ambiguous")
        baseline = self.scan("baseline")
        fixture = baseline["INBOX"][0]
        expected = identity(fixture)
        addresses = sender_account.get("emailAddresses", []) + [sender_account.get("emailAddress", "")]
        require(parseaddr(fixture["sender"])[1].casefold() in
                {address.strip().casefold() for address in addresses if address.strip()},
                "fixture sender must belong to the selected configured sender account")
        # Validate every pre-existing destination copy against the same identity.
        for row in baseline[self.args.destination]:
            require(identity(row) == expected, "baseline destination has a marker collision")
        self.state["identity"] = expected
        self.save()
        arguments = ["messages", "batch", "archive", self.args.message_id,
                     "-a", self.args.account, "-m", "INBOX", "--mark-read"]
        preview = self.run("preview", [*arguments, "--dry-run"])
        self.receipt(preview, expected, dry_run=True)
        if not self.args.apply:
            self.state["status"] = "dry-run-passed"
            return
        self.scan("pre-mutation", expected)
        report = self.evidence / "archive-receipt.json"
        self.state["archiveAttempted"] = True
        self.save()
        applied = self.run("archive", [*arguments, "--verify", "--settle", "10s",
                                       "--verify-budget", "30s", "--report-file", str(report)])
        self.receipt(applied, expected)
        self.scan("immediate-after-archive", expected, archived=True)
        sync = self.run("sync", ["sync", "-a", self.args.account, "-m", "INBOX",
                                 "--wait", "--timeout", "20"])
        require(sync.get("account") == self.args.account and sync.get("status") == "completed",
                "explicit sync did not complete")
        self.state["sync"] = sync
        self.state["syncEndedAt"] = timestamp()
        self.save()
        started = time.monotonic()
        observations = []
        while True:
            sampled_after = time.monotonic() - started
            members = self.scan("post-sync-scan", expected, archived=True)
            reconciled = self.run("post-sync-reconcile", ["messages", "reconcile", str(report),
                                                          "--settle", "2s", "--verify-budget", "20s"])
            self.receipt(reconciled, expected)
            elapsed = time.monotonic() - started
            observations.append({"observedAt": timestamp(), "sampleStartedAfterSyncSeconds": sampled_after,
                                 "elapsedSeconds": elapsed,
                                 "destinationLocalId": members[self.args.destination][0]["id"]})
            self.state["observations"] = observations
            self.save()
            print(f"archive-sync-check: observation {len(observations)} passed ({elapsed:.1f}s after sync)", flush=True)
            # A final pair of observations must BEGIN after the full window;
            # command latency is not evidence that an earlier sample stayed true.
            if sampled_after >= self.args.settle_seconds and len(observations) >= 2:
                break
            delay = min(self.args.interval_seconds, max(0, self.args.settle_seconds - elapsed))
            require(self.deadline - time.monotonic() > delay, "overall settling budget exhausted")
            # A pause is bounded to 30 seconds; no long blocking Mail calls.
            time.sleep(delay)
        self.state["status"] = "passed"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--new-marker", action="store_true", help="Print a UUID fixture subject; does not open Mail")
    parser.add_argument("--cli", default="mail-app-cli", help="mail-app-cli executable, PATH name or absolute path")
    parser.add_argument("--account")
    parser.add_argument("--sender-account", help="Own configured sender account (default: target --account)")
    parser.add_argument("--message-id", help="One existing disposable fixture's positive INBOX local ID")
    parser.add_argument("--marker", help=f"Exact fixture subject: {PREFIX}<UUID>")
    parser.add_argument("--destination", default="All Mail")
    parser.add_argument("--evidence-dir", help="Absolute new private directory outside the repository")
    parser.add_argument("--apply", action="store_true", help="Actually mark the fixture read, archive, and request sync")
    parser.add_argument("--confirm-disposable", action="store_true", help="Assert this is a user-owned disposable fixture")
    parser.add_argument("--settle-seconds", type=int, default=60)
    parser.add_argument("--interval-seconds", type=int, default=15)
    parser.add_argument("--command-timeout", type=int, default=60)
    parser.add_argument("--budget-seconds", type=int, default=300)
    args = parser.parse_args()
    if args.new_marker:
        print(PREFIX + str(uuid.uuid4()))
        return 0
    check = None
    try:
        require(args.account and args.account.strip() == args.account and args.marker and args.message_id,
                "--account, --message-id, and --marker are required")
        require(args.sender_account is None or
                (args.sender_account and args.sender_account.strip() == args.sender_account),
                "--sender-account must be a nonempty exact account name")
        require(re.fullmatch(r"[1-9][0-9]*", args.message_id), "message ID must be a positive local ID")
        require(args.marker.startswith(PREFIX) and str(uuid.UUID(args.marker[len(PREFIX):])) == args.marker[len(PREFIX):],
                "marker must be the exact generated UUID fixture subject")
        require(args.destination and args.destination != "INBOX", "destination must differ from INBOX")
        require(not args.apply or args.confirm_disposable, "--apply requires --confirm-disposable")
        require(60 <= args.settle_seconds <= 300 and 1 <= args.interval_seconds <= 30 and
                1 <= args.command_timeout <= 60 and 90 <= args.budget_seconds <= 900,
                "settle must be 60..300s, interval 1..30s, command timeout 1..60s, budget 90..900s")
        require(args.evidence_dir and Path(args.evidence_dir).is_absolute(),
                "--evidence-dir must be an absolute new private directory")
        evidence = Path(args.evidence_dir).resolve()
        repo = Path(__file__).resolve().parent.parent
        require(evidence != repo and repo not in evidence.parents, "evidence must stay outside the repository")
        require(not evidence.exists(), "evidence directory already exists; choose a new path")
        require(Path(args.cli).name == "mail-app-cli", "the executable must be named mail-app-cli")
        executable = shutil.which(args.cli)
        require(executable is not None, "mail-app-cli executable not found")
        args.cli = str(Path(executable).resolve())
        os.umask(0o077)
        evidence.mkdir(parents=True, mode=0o700)
        check = Check(args, evidence)
        print(f"archive-sync-check: private evidence {evidence}", flush=True)
        check.execute()
        check.state["endedAt"] = timestamp()
        check.save()
        print(f"archive-sync-check: {check.state['status']}; fixture retained; evidence {evidence}")
        return 0
    except (CheckFailed, ValueError, OSError, KeyError, TypeError, KeyboardInterrupt) as exc:
        if check is not None:
            check.state.update(status="failed", endedAt=timestamp(), error=str(exc) or "interrupted")
            check.save()
        print(f"archive-sync-check: failed: {str(exc) or 'interrupted'}; fixture retained; no mutation replay", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
