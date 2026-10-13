"""Offline harness tests: mocked Mail operations and one technical process shim."""

import copy
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location(
    "archive_sync_check", Path(__file__).with_name("archive-sync-check.py"))
check_module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(check_module)


class Clock:
    def __init__(self):
        self.now = 0

    def monotonic(self):
        return self.now

    def sleep(self, seconds):
        self.now += seconds


class ArchiveSyncCheckTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="archive-sync-offline-")
        self.addCleanup(self.temporary.cleanup)
        self.clock = Clock()
        self.monotonic = patch.object(check_module.time, "monotonic", self.clock.monotonic)
        self.sleep = patch.object(check_module.time, "sleep", self.clock.sleep)
        self.monotonic.start()
        self.sleep.start()
        self.addCleanup(self.monotonic.stop)
        self.addCleanup(self.sleep.stop)
        self.args = SimpleNamespace(
            account="Fixture Account", sender_account=None, destination="All Mail", message_id="101",
            marker="mail-app-cli-archive-fixture-12345678-1234-4234-8234-123456789abc",
            cli="/unused/mail-app-cli", apply=True, budget_seconds=300,
            command_timeout=60, settle_seconds=60, interval_seconds=15)
        self.check = check_module.Check(self.args, Path(self.temporary.name))
        self.calls = []
        self.mailboxes = ["INBOX", "All Mail"]
        self.fixture = dict(id="101", account=self.args.account, mailbox="INBOX",
                            subject=self.args.marker, sender="Fixture <fixture@example.test>",
                            dateSent="2026-10-13T00:00:00Z", messageSize=300,
                            read=False, deleted=False, mailboxes=["INBOX"])
        self.expected = check_module.identity(self.fixture)
        item = dict(self.fixture, sourceMailbox="INBOX", targetMailbox="All Mail",
                    status="succeeded", identity=self.expected,
                    observed=dict(account=self.args.account, sourceMailbox="INBOX",
                                  destinationMailbox="All Mail", sourceRemovalRequired=True,
                                  sourcePresent=False, destinationPresent=True, destinationRead=True))
        self.receipt = dict(action="archive", dryRun=False, matched=1, attempted=1,
                            succeeded=1, failed=0, skipped=0, items=[item])
        self.check.run = self.fake_run

    def scan(self, archived=False):
        messages = [dict(self.fixture)]
        if archived:
            messages = [dict(self.fixture, id="202", read=True,
                             mailbox=self.args.destination, mailboxes=[self.args.destination])]
        return dict(complete=True, messages=messages,
                    coverage=[dict(account=self.args.account, mailbox=box, exhausted=True,
                                   truncated=False, remaining=0) for box in ("INBOX", self.args.destination)])

    def fake_run(self, label, arguments):
        self.calls.append((label, arguments))
        if label == "version":
            return {"version": "offline"}
        if label == "account":
            return dict(name=self.args.account, enabled=True, emailAddress="fixture@example.test")
        if label == "sender-account":
            return dict(name=self.args.sender_account, enabled=True, emailAddress="sender@example.test")
        if label == "mailboxes":
            return [dict(account=self.args.account, name=box) for box in self.mailboxes]
        if label == "preview":
            item = dict(self.receipt["items"][0], status="dry-run")
            return dict(action="archive", dryRun=True, matched=1, attempted=0,
                        items=[item])
        if label in ("archive", "post-sync-reconcile"):
            return copy.deepcopy(self.receipt)
        if label == "sync":
            return dict(account=self.args.account, status="completed", actualScope="account")
        return self.scan(archived=label not in ("baseline", "pre-mutation"))

    def test_applied_sequence_waits_before_final_observation(self):
        with patch("builtins.print"):
            self.check.execute()
        self.assertEqual(self.check.state["status"], "passed")
        labels = [label for label, _ in self.calls]
        self.assertEqual(labels.count("archive"), 1)
        self.assertEqual(labels.count("sync"), 1)
        self.assertGreater(labels.index("sync"), labels.index("immediate-after-archive"))
        self.assertGreaterEqual(self.check.state["observations"][-1]["sampleStartedAfterSyncSeconds"], 60)
        self.assertGreaterEqual(labels.count("post-sync-reconcile"), 2)
        self.assertFalse(any(command[0] in ("send", "delete", "import") for _, command in self.calls))

    def test_preview_does_not_mutate_or_sync(self):
        self.args.apply = False
        self.check.execute()
        self.assertEqual(self.check.state["status"], "dry-run-passed")
        self.assertFalse(self.check.state["archiveAttempted"])
        self.assertFalse({label for label, _ in self.calls} & {"archive", "sync", "post-sync-reconcile"})

    def test_preview_without_target_mailbox_blocks_archive(self):
        original = self.fake_run
        def legacy_preview(label, arguments):
            data = original(label, arguments)
            if label == "preview":
                data["items"][0].pop("targetMailbox", None)
            return data
        self.check.run = legacy_preview
        with self.assertRaisesRegex(check_module.CheckFailed, "preview archive destination is missing or differs"):
            self.check.execute()
        self.assertFalse(self.check.state["archiveAttempted"])
        self.assertFalse({label for label, _ in self.calls} & {"archive", "sync"})

    def test_archive_selection_rejects_mismatch_before_mutation(self):
        self.args.destination = "Archive"
        self.mailboxes = ["INBOX", "Archive", "All Mail"]
        with self.assertRaisesRegex(check_module.CheckFailed, "preview archive destination is missing or differs"):
            self.check.execute()
        self.assertFalse(self.check.state["archiveAttempted"])
        self.assertIn("preview", {label for label, _ in self.calls})
        self.assertFalse({label for label, _ in self.calls} & {"archive", "sync"})

    def test_resolver_all_mail_blocks_archive_when_listing_only_shows_archive(self):
        self.args.destination = "Archive"
        self.mailboxes = ["INBOX", "Archive"]
        # The real recursive resolver can find nested All Mail omitted by the
        # mailbox-list fallback; the returned preview target must decide.
        with self.assertRaisesRegex(check_module.CheckFailed, "preview archive destination is missing or differs"):
            self.check.execute()
        self.assertFalse(self.check.state["archiveAttempted"])
        self.assertIn("preview", {label for label, _ in self.calls})
        self.assertFalse({label for label, _ in self.calls} & {"archive", "sync"})

    def test_all_mail_is_preferred_when_archive_also_exists(self):
        self.args.apply = False
        self.mailboxes = ["INBOX", "Archive", "All Mail"]
        self.check.execute()
        self.assertEqual(self.check.state["status"], "dry-run-passed")

    def test_archive_only_account_passes_with_resolved_preview(self):
        self.args.destination = "Archive"
        self.mailboxes = ["INBOX", "Archive"]
        item = self.receipt["items"][0]
        item["targetMailbox"] = "Archive"
        item["observed"]["destinationMailbox"] = "Archive"
        with patch("builtins.print"):
            self.check.execute()
        self.assertEqual(self.check.state["status"], "passed")
        self.assertEqual(sum(label == "archive" for label, _ in self.calls), 1)

    def test_absent_or_ambiguous_archive_mailbox_is_rejected_before_mutation(self):
        for boxes in (["INBOX"], ["INBOX", "All Mail", "All Mail"]):
            with self.subTest(boxes=boxes):
                self.mailboxes = boxes
                self.calls.clear()
                with self.assertRaisesRegex(check_module.CheckFailed, "absent or ambiguous"):
                    self.check.execute()
                self.assertFalse(self.check.state["archiveAttempted"])
                self.assertFalse({label for label, _ in self.calls} & {"preview", "archive", "sync"})

    def test_fixture_from_another_own_configured_account_is_allowed(self):
        self.args.apply = False
        self.args.sender_account = "Own Sender Account"
        self.fixture["sender"] = "Own Sender <sender@example.test>"
        self.expected = check_module.identity(self.fixture)
        self.receipt["items"][0].update(sender=self.fixture["sender"], identity=self.expected)
        self.check.execute()
        self.assertEqual(self.check.state["status"], "dry-run-passed")
        self.assertIn(("sender-account", ["accounts", "show", "Own Sender Account"]), self.calls)

    def test_external_sender_is_rejected_before_archive_preview(self):
        self.args.sender_account = "Own Sender Account"
        self.fixture["sender"] = "Unknown <external@example.test>"
        with self.assertRaisesRegex(check_module.CheckFailed, "configured sender account"):
            self.check.execute()
        self.assertFalse({label for label, _ in self.calls} & {"preview", "archive", "sync"})

    def test_slow_pre_window_pair_requires_a_new_post_window_pair(self):
        durations = iter([1, 50, 1])
        def slow_run(label, arguments):
            data = self.fake_run(label, arguments)
            if label == "post-sync-reconcile":
                self.clock.now += next(durations)
            return data
        self.check.run = slow_run
        with patch("builtins.print"):
            self.check.execute()
        observations = self.check.state["observations"]
        self.assertEqual(len(observations), 3)
        self.assertLess(observations[1]["sampleStartedAfterSyncSeconds"], 60)
        self.assertGreaterEqual(observations[1]["elapsedSeconds"], 60)
        self.assertGreaterEqual(observations[2]["sampleStartedAfterSyncSeconds"], 60)

    def test_recurrence_fails_without_replaying_archive(self):
        def recurring(label, arguments):
            data = self.fake_run(label, arguments)
            if label == "post-sync-scan" and self.clock.now >= 15:
                data["messages"].append(dict(self.fixture, id="303", read=True))
            return data
        self.check.run = recurring
        with patch("builtins.print"), self.assertRaisesRegex(check_module.CheckFailed, "INBOX"):
            self.check.execute()
        self.assertEqual(sum(label == "archive" for label, _ in self.calls), 1)

    def test_missing_changed_duplicate_or_incomplete_identity_fails(self):
        variations = [dict(self.scan(True), messages=[]),
                      dict(self.scan(True), complete=False),
                      dict(self.scan(True), messages=[dict(self.fixture, messageSize=999)]),
                      dict(self.scan(True), messages=self.scan(True)["messages"] * 2)]
        for data in variations:
            with self.subTest(data=data):
                self.check.run = lambda *unused: data
                with self.assertRaises(check_module.CheckFailed):
                    self.check.scan("bad-observation", self.expected, archived=True)

    def test_command_timeout_persists_logs_and_never_retries(self):
        self.check.run = check_module.Check.run.__get__(self.check)
        process = unittest.mock.Mock(pid=43210)
        process.wait.side_effect = [subprocess.TimeoutExpired("fixture", 60), 7]
        with patch.object(check_module.subprocess, "Popen", return_value=process) as runner, \
                patch.object(check_module.os, "killpg") as kill_group:
            with self.assertRaisesRegex(check_module.CheckFailed, "never replay"):
                self.check.run("archive", ["messages", "batch", "archive", "101"])
        runner.assert_called_once()
        self.assertTrue(runner.call_args.kwargs["start_new_session"])
        kill_group.assert_called_once_with(43210, check_module.signal.SIGTERM)
        evidence = Path(self.temporary.name)
        metadata = json.loads((evidence / "001-archive.step.json").read_text())
        self.assertIsNone(metadata["exitCode"])
        self.assertTrue(metadata["timedOut"])
        self.assertEqual(metadata["stoppedExitCode"], 7)
        self.assertEqual(metadata["shutdownMode"], "graceful")
        self.assertIn("startedAt", metadata)
        self.assertIn("endedAt", metadata)
        self.assertTrue((evidence / "001-archive.stdout").is_file())
        self.assertTrue((evidence / "001-archive.stderr").is_file())

    def test_timeout_escalates_only_after_grace_and_marks_cleanup_unconfirmed(self):
        self.check.run = check_module.Check.run.__get__(self.check)
        process = unittest.mock.Mock(pid=43210)
        process.wait.side_effect = [subprocess.TimeoutExpired("fixture", 60),
                                    subprocess.TimeoutExpired("fixture", 5), -9]
        events = []
        process.wait.side_effect = iter(process.wait.side_effect)
        outcomes = process.wait.side_effect
        def ordered_wait(timeout):
            events.append(("wait", timeout))
            result = next(outcomes)
            if isinstance(result, Exception):
                raise result
            return result
        process.wait.side_effect = ordered_wait
        with patch.object(check_module.subprocess, "Popen", return_value=process), \
                patch.object(check_module.os, "killpg", side_effect=lambda pid, sig: events.append(("signal", sig))):
            with self.assertRaises(check_module.CheckFailed):
                self.check.run("archive", ["messages", "batch", "archive", "101"])
        self.assertEqual(events, [("wait", 60), ("signal", check_module.signal.SIGTERM),
                                  ("wait", 5), ("signal", check_module.signal.SIGKILL), ("wait", 2)])
        metadata = json.loads((Path(self.temporary.name) / "001-archive.step.json").read_text())
        self.assertEqual(metadata["stoppedExitCode"], -9)
        self.assertEqual(metadata["shutdownMode"], "hard-stop-cleanup-unconfirmed")
        self.assertTrue(metadata["bridgeCleanupUnconfirmed"])

    def test_real_offline_cli_term_handler_stops_its_detached_child(self):
        evidence = Path(self.temporary.name)
        shim = evidence / "mail-app-cli"
        shim.write_text("#!" + sys.executable + "\n" + '''
import json, os, pathlib, signal, subprocess, sys, time
root = pathlib.Path(__file__).parent
child = subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(60)'], start_new_session=True)
def stop(signum, frame):
    os.killpg(child.pid, signal.SIGTERM)
    code = child.wait(timeout=2)
    (root / 'child-exited.json').write_text(json.dumps({'exitCode': code}))
    sys.exit(7)
signal.signal(signal.SIGTERM, stop)
(root / 'owned-pids.json').write_text(json.dumps({'parent': os.getpid(), 'child': child.pid}))
time.sleep(60)
''')
        shim.chmod(0o700)
        self.args.cli = str(shim)
        self.args.command_timeout = 1
        self.check.run = check_module.Check.run.__get__(self.check)
        with self.assertRaisesRegex(check_module.CheckFailed, "never replay"):
            self.check.run("offline-process-shim", [])
        metadata = json.loads((evidence / "001-offline-process-shim.step.json").read_text())
        self.assertEqual(metadata["shutdownMode"], "graceful")
        self.assertEqual(metadata["stoppedExitCode"], 7)
        self.assertEqual(json.loads((evidence / "child-exited.json").read_text())["exitCode"],
                         -check_module.signal.SIGTERM)
        owned = json.loads((evidence / "owned-pids.json").read_text())
        for pid in owned.values():
            self.assertIsInstance(pid, int)
            self.assertGreater(pid, 0)
            with self.assertRaises(ProcessLookupError):
                os.kill(pid, 0)


if __name__ == "__main__":
    unittest.main()
