#!/usr/bin/env python3
"""Capture the released CLI and TUI with a disposable, fictional Mail fixture.

Requires macOS, mail-app-cli 3.1.0, tmux, pyte, and Playwright with Chromium.
No real Mail data or settings are used. The CLI itself reads the synthetic
Envelope Index and MIME files. A fail-closed osascript shim supplies only
fictional account inventory and read-only message metadata.
"""

import argparse
import datetime as dt
import errno
import fcntl
import hashlib
import html
import json
import os
from pathlib import Path
import pty
import re
import shlex
import shutil
import sqlite3
import struct
import subprocess
import tempfile
import termios
import time
from email.message import EmailMessage
from email.policy import SMTP

import pyte
from playwright.sync_api import sync_playwright


REPO = Path(__file__).resolve().parent.parent
OUTPUT = REPO / "docs/images/v3.1.0"
FIXTURE = REPO / "scripts/fixtures/publication.json"
PALETTE = {
    "black": "#111318", "red": "#e87979", "green": "#a6cf8c",
    "brown": "#e4c07c", "yellow": "#e4c07c", "blue": "#77a9ee",
    "magenta": "#c59ce5", "cyan": "#7fc8c2", "white": "#d4d8df",
    "brightblack": "#78818e", "brightred": "#f08e8e",
    "brightgreen": "#b6dd9a", "brightbrown": "#f1d495",
    "brightyellow": "#f1d495", "brightblue": "#96bcf2",
    "brightmagenta": "#d3b0ed", "brightcyan": "#9fdad5",
    "brightwhite": "#f1f3f6",
}


def run(args, **kwargs):
    return subprocess.run(args, check=True, capture_output=True, text=True, **kwargs).stdout


def make_fixture(root, unsubscribe_demo=False):
    data = json.loads(FIXTURE.read_text())
    if unsubscribe_demo:
        for item in data["messages"]:
            item.update(data["unsubscribeExamples"].get(item["email"], {}))
    version = root / "V12"
    index = version / "MailData/Envelope Index"
    index.parent.mkdir(parents=True, mode=0o700)
    db = sqlite3.connect(index)
    db.executescript("""
        create table mailboxes(url text,total_count integer,unread_count integer);
        create table subjects(subject text);
        create table addresses(address text,comment text);
        create table summaries(summary text);
        create table messages(subject integer,sender integer,summary integer,
            mailbox integer,date_received integer,date_sent integer,read integer,
            flagged integer,deleted integer,size integer,subject_prefix text default '');
        create table labels(message_id integer,mailbox_id integer);
    """)
    accounts = {a["name"]: a for a in data["accounts"]}
    boxes = {}
    metadata = {}
    for account in data["accounts"]:
        for box in ["INBOX", "Archive", "Drafts", "Sent", "Trash"]:
            rows = [m for m in data["messages"] if m["account"] == account["name"]] if box == "INBOX" else []
            cur = db.execute("insert into mailboxes values (?,?,?)", (
                f'imap://{account["id"]}/{box}', len(rows), sum(not m["read"] for m in rows)))
            boxes[account["name"], box] = cur.lastrowid
    for item in data["messages"]:
        message = EmailMessage(policy=SMTP)
        message["From"] = f'{item["sender"]} <{item["email"]}>'
        message["To"] = f'Alex Rivera <{accounts[item["account"]]["emailAddress"]}>'
        message["Subject"] = item["subject"]
        date = dt.datetime.fromisoformat(item["date"])
        message["Date"] = date
        message["Message-ID"] = f'<demo-{item["id"]}@example.test>'
        body = item["body"]
        rich = item.get("html", "<p>" + html.escape(body).replace("\n\n", "</p><p>").replace("\n", "<br>") + "</p>")
        if item.get("listId"):
            message["List-Id"] = f'{item["sender"]} <{item["listId"]}>'
            message["List-Unsubscribe"] = f'<{item["unsubscribe"]}>'
            if item.get("oneClick"):
                message["List-Unsubscribe-Post"] = "List-Unsubscribe=One-Click"
            body += "\n\nUnsubscribe: " + item["unsubscribe"]
            rich += f'<p><a href="{item["unsubscribe"]}">Unsubscribe here</a></p>'
            if item.get("preferences"):
                rich += f'<p><a href="{item["preferences"]}">Manage preferences</a></p>'
        message.set_content(body)
        message.add_alternative(rich, subtype="html")
        raw = message.as_bytes()
        metadata[str(item["id"])] = {
            "id": str(item["id"]), "account": item["account"], "mailbox": "INBOX",
            "subject": item["subject"], "sender": str(message["From"]),
            "dateSent": date.isoformat(), "dateReceived": date.isoformat(),
            "read": item["read"], "flagged": item.get("flagged", False),
            "deleted": False, "messageSize": len(raw), "content": "",
            "toRecipients": [accounts[item["account"]]["emailAddress"]],
            "ccRecipients": [], "bccRecipients": [],
        }
        parts = []
        number = item["id"] // 1000
        while number:
            parts.append(str(number % 10))
            number //= 10
        owner = version / accounts[item["account"]]["id"] / "INBOX.mbox/DEMO/Data"
        path = owner.joinpath(*parts, "Messages", f'{item["id"]}.emlx')
        path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        path.write_bytes(str(len(raw)).encode() + b"\n" + raw)
        subject = db.execute("insert into subjects values (?)", (item["subject"],)).lastrowid
        sender = db.execute("insert into addresses values (?,?)", (item["email"], item["sender"])).lastrowid
        summary = db.execute("insert into summaries values (?)", (item["body"].split("\n\n")[1],)).lastrowid
        stamp = int(date.timestamp())
        db.execute("insert into messages(ROWID,subject,sender,summary,mailbox,date_received,date_sent,read,flagged,deleted,size) values (?,?,?,?,?,?,?,?,?,?,?)", (
            item["id"], subject, sender, summary, boxes[item["account"], "INBOX"], stamp, stamp,
            int(item["read"]), int(item.get("flagged", False)), 0, len(raw)))
    db.commit()
    db.close()
    shim = root / "shim"
    shim.mkdir(mode=0o700)
    (shim / "accounts.json").write_text(json.dumps(data["accounts"]))
    (shim / "messages.json").write_text(json.dumps(metadata))
    (shim / "osascript").write_text(r'''#!/usr/bin/env python3
import json, pathlib, re, sys
root = pathlib.Path(__file__).resolve().parent
script = " ".join(sys.argv[1:])
operation = "blocked"
payload = None
if "const accounts = mail.accounts();" in script and "emailAddresses" in script:
    operation = "fixture_accounts"
    payload = json.loads((root / "accounts.json").read_text())
elif "JSON.stringify({message: message, headerText: headerText, failure: failure, phases: phaseLog});" in script and "const skipContent = true;" in script:
    mid = re.search(r"const requestedId = '([0-9]+)';", script)
    account = re.search(r"const requestedAccount = '([A-Za-z]+)';", script)
    messages = json.loads((root / "messages.json").read_text())
    item = messages.get(mid[1]) if mid else None
    if item and account and item["account"] == account[1] and "const requestedMailbox = 'INBOX';" in script:
        operation = "fixture_message_metadata"
        payload = {"message": item, "headerText": None, "failure": None, "phases": []}
with (root / "automation.jsonl").open("a") as log:
    log.write(json.dumps({"operation": operation}) + "\n")
if payload is None:
    sys.stderr.write("Publication fixture blocked an unsupported automation call.\n")
    sys.exit(84)
sys.stdout.write(json.dumps(payload))
''')
    (shim / "osascript").chmod(0o755)
    (root / "config.json").write_text("{}")
    env = os.environ.copy()
    for key in list(env):
        if key.startswith("MAIL_APP_CLI_"):
            del env[key]
    env.update({
        "PATH": str(shim) + os.pathsep + os.environ["PATH"],
        "MAIL_APP_CLI_BACKEND": "legacy", "MAIL_APP_CLI_BODY_SOURCE": "disk",
        "MAIL_APP_CLI_ENVELOPE_INDEX_PATH": str(index),
        "MAIL_APP_CLI_CONFIG": str(root / "config.json"),
        "MAIL_APP_CLI_AUTOMATION_LOCK_PATH": str(root / "automation.lock"),
        "TERM": "xterm-256color", "COLORTERM": "truecolor", "TZ": "Asia/Bangkok",
    })
    env.pop("NO_COLOR", None)
    return env


def capture_command(binary, args, env, cwd, columns=132):
    master, slave = pty.openpty()
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 40, columns, 0, 0))
    process = subprocess.Popen([binary, *args], stdin=subprocess.DEVNULL,
                               stdout=slave, stderr=slave, env=env, cwd=cwd)
    os.close(slave)
    chunks = []
    try:
        while True:
            try:
                part = os.read(master, 65536)
            except OSError as error:
                if error.errno == errno.EIO:
                    break
                raise
            if not part:
                break
            chunks.append(part)
    finally:
        os.close(master)
    text = b"".join(chunks).decode()
    if process.wait(timeout=15) != 0:
        raise RuntimeError(text)
    return text.replace("\r\n", "\n")


def capture_tui(binary, env, root):
    socket = root / "terminal.sock"
    tmux = [shutil.which("tmux"), "-2", "-S", str(socket), "-f", "/dev/null"]
    command = shlex.join([binary, "tui", "--message", "100246"])
    try:
        run([*tmux, "new-session", "-d", "-s", "publication", "-x", "152", "-y", "32", command], cwd=root, env=env)
        run([*tmux, "set-option", "-g", "status", "off"])
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            text = run([*tmux, "capture-pane", "-p", "-e", "-t", "publication:0.0"])
            if "Three habits" in text and "Cedar Weekly" in text and "could not load body" not in text:
                return text
            time.sleep(0.3)
        raise RuntimeError("Fixture TUI did not finish loading:\n" + text)
    finally:
        subprocess.run([*tmux, "kill-server"], capture_output=True, check=False)


def terminal_screen(text, columns, height):
    screen = pyte.Screen(columns, height)
    pyte.Stream(screen).feed(text.rstrip("\r\n").replace("\r\n", "\n").replace("\n", "\r\n"))
    return screen


def css_color(value, default):
    if value == "default":
        return default
    if value in PALETTE:
        return PALETTE[value]
    if re.fullmatch(r"[0-9a-fA-F]{6}", value):
        return "#" + value
    raise ValueError("Unknown ANSI color: " + value)


def screen_html(screen, rows):
    lines = []
    for row in range(rows):
        runs = []
        previous = None
        content = ""
        for col in range(screen.columns):
            char = screen.buffer[row][col]
            fg = css_color(char.fg, "#d4d8df")
            bg = css_color(char.bg, "#15181e")
            if char.reverse:
                fg, bg = bg, fg
            style = f"color:{fg};background:{bg};"
            if char.bold:
                style += "font-weight:700;"
            if char.italics:
                style += "font-style:italic;"
            if char.underscore:
                style += "text-decoration:underline;"
            if previous is not None and style != previous:
                runs.append(f'<span style="{previous}">{html.escape(content)}</span>')
                content = ""
            content += char.data
            previous = style
        if previous:
            runs.append(f'<span style="{previous}">{html.escape(content)}</span>')
        lines.append("".join(runs))
    return "\n".join(lines)


def render(page, filename, title, text, columns, font, line_height, tui=False):
    screen = terminal_screen(text, columns, 32 if tui else 100)
    rows = 32 if tui else max(i + 1 for i, line in enumerate(screen.display) if line.strip())
    if rows * line_height > 760:
        raise RuntimeError(f"{filename}: {rows} rows would clip; adjust typography")
    content = screen_html(screen, rows)
    page.set_content(f'''<!doctype html><html><head><meta charset="utf-8"><style>
      * {{ box-sizing: border-box; }}
      html, body {{ margin:0; width:1600px; height:900px; overflow:hidden; }}
      body {{ background:#090b0e; display:flex; align-items:center; justify-content:center; }}
      .terminal {{ width:1520px; background:#15181e; border:1px solid #30353e;
        border-radius:18px; overflow:hidden; box-shadow:0 24px 80px #0008; }}
      .bar {{ height:58px; display:flex; align-items:center; padding:0 25px;
        background:#1b1e25; border-bottom:1px solid #2b3039; position:relative; }}
      .dots {{ display:flex; gap:10px; }} .dots i {{ width:13px;height:13px;border-radius:50%;background:#f07178; }}
      .dots i:nth-child(2) {{ background:#e8c16d; }} .dots i:nth-child(3) {{ background:#a3c985; }}
      .title {{ position:absolute;left:150px;right:150px;text-align:center;color:#aeb5c0;
        font:500 17px -apple-system,BlinkMacSystemFont,sans-serif;letter-spacing:.15px; }}
      .badge {{ margin-left:auto;color:#929ba9;font:13px -apple-system,sans-serif; }}
      pre {{ margin:0; padding:26px 28px 30px; color:#d4d8df;
        font:{font}px/{line_height}px Menlo,monospace;font-variant-ligatures:none;white-space:pre; }}
    </style></head><body><div class="terminal"><div class="bar"><div class="dots"><i></i><i></i><i></i></div>
      <div class="title">mail-app-cli 3.1.0 · {html.escape(title)}</div><div class="badge">Fictional demo mail</div></div>
      <pre>{content}</pre></div></body></html>''')
    page.evaluate("document.fonts.ready")
    if page.locator("pre").evaluate("el => el.scrollWidth > el.clientWidth"):
        raise RuntimeError(filename + ": terminal content would clip horizontally")
    destination = OUTPUT / filename
    assert destination.is_absolute() and destination.parent == OUTPUT and not destination.is_symlink()
    page.screenshot(path=str(destination), animations="disabled")
    (OUTPUT / filename.replace(".png", ".txt")).write_text("\n".join(line.rstrip() for line in screen.display[:rows]).rstrip() + "\n")


def prompt(command):
    return "\x1b[32m❯\x1b[0m " + command + "\n"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", default=shutil.which("mail-app-cli"))
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve()) if args.binary else ""
    if not binary or not Path(binary).is_file() or not shutil.which("tmux"):
        parser.error("mail-app-cli and tmux must be installed")
    version = json.loads(run([binary, "version"]))["data"]["version"]
    if version != "3.1.0":
        parser.error("captures are pinned to released mail-app-cli 3.1.0")
    root = Path(tempfile.mkdtemp(prefix="mail-demo-", dir="/tmp"))
    root.chmod(0o700)
    env = make_fixture(root)
    unsubscribe_root = root / "unsubscribe"
    unsubscribe_root.mkdir(mode=0o700)
    unsubscribe_env = make_fixture(unsubscribe_root, unsubscribe_demo=True)
    OUTPUT.mkdir(parents=True, exist_ok=True)
    if OUTPUT.resolve() != OUTPUT or not OUTPUT.is_dir():
        raise RuntimeError("Output directory must be a real directory inside the repo")
    human = prompt("mail-app-cli inbox --limit 5")
    human += capture_command(binary, ["inbox", "--limit", "5"], env, root)
    human += "\n" + prompt("mail-app-cli links 100248")
    human += capture_command(binary, ["links", "100248"], env, root)
    query = (
        ".data | {complete, inspected: .readability.inspected,\n"
        "    lists: [.groups[] | {listId, messageCount,\n"
        '      unsubscribe: [.destinations[] | select(.kind == "unsubscribe") | .url]}]}'
    )
    agent = prompt("mail-app-cli unsubscribe scan INBOX --all-accounts \\\n  --checkpoint ./agent.json \\\n  --jq '" + query + "'")
    agent += capture_command(binary, ["unsubscribe", "scan", "INBOX", "--all-accounts", "--checkpoint", "./agent.json", "--jq", query], unsubscribe_env, unsubscribe_root)
    unsubscribe = prompt("mail-app-cli unsubscribe show 100246")
    unsubscribe += capture_command(binary, ["unsubscribe", "show", "100246"], unsubscribe_env, unsubscribe_root)
    unsubscribe += "\n" + prompt("mail-app-cli unsubscribe scan INBOX --all-accounts \\\n  --checkpoint ./unsubscribe.json")
    unsubscribe += capture_command(binary, ["unsubscribe", "scan", "INBOX", "--all-accounts", "--checkpoint", "./unsubscribe.json"], unsubscribe_env, unsubscribe_root)
    tui = capture_tui(binary, env, root)
    logs = [root / "shim/automation.jsonl", unsubscribe_root / "shim/automation.jsonl"]
    events = [json.loads(line) for log in logs for line in log.read_text().splitlines()]
    if any(event["operation"] not in {"fixture_accounts", "fixture_message_metadata"} for event in events):
        raise RuntimeError("Capture attempted forbidden automation")
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        page = browser.new_page(viewport={"width": 1600, "height": 900}, device_scale_factor=2)
        page.route("**/*", lambda route: route.abort())
        render(page, "human-cli.png", "Human CLI", human, 100, 24, 34)
        render(page, "agent-cli.png", "Agent CLI · structured discovery", agent, 132, 17, 21)
        render(page, "unsubscribe.png", "Unsubscribe discovery", unsubscribe, 142, 17, 21.5)
        render(page, "tui.png", "Interactive mail client", tui, 152, 15.75, 22, tui=True)
        browser.close()
    for path in sorted(OUTPUT.glob("*.png")):
        print(path.relative_to(REPO), hashlib.sha256(path.read_bytes()).hexdigest()[:12])
    print("All mail is fictional. Fixture directory:", root)


if __name__ == "__main__":
    main()
