"""Run loopback-only WeCom and Feishu placeholders for isolated acceptance.

Example: python scripts/high_risk_notification_receiver.py --port 18191 --fail-first
Ready and delivery records are emitted as JSON lines on stdout.
"""

import argparse
import json
import threading
import uuid
from datetime import datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


CHANNELS = {"/wecom": "WECOM", "/feishu": "FEISHU"}
EVENTS = {"APPROVAL_REQUIRED", "APPROVED", "REJECTED"}
NOTICE_FIELDS = {"notificationId", "actionId", "recipientId", "channel", "event", "expiresAt"}


def validate_notice(notice, path):
    if not isinstance(notice, dict) or path not in CHANNELS:
        raise ValueError
    event = notice.get("event")
    if not isinstance(event, str) or event not in EVENTS or notice.get("channel") != CHANNELS[path]:
        raise ValueError
    fields = NOTICE_FIELDS | {"claimPath"} if event == "APPROVED" else NOTICE_FIELDS
    if notice.keys() != fields:
        raise ValueError
    for key in ("notificationId", "actionId", "recipientId"):
        if not isinstance(notice[key], str) or str(uuid.UUID(notice[key])) != notice[key]:
            raise ValueError
    expiry = datetime.fromisoformat(notice["expiresAt"].replace("Z", "+00:00"))
    if expiry.tzinfo is None:
        raise ValueError
    if event == "APPROVED":
        claim_path = f"/console/api/v2/self-service/write-requests/{notice['actionId']}/authorization-code"
        if notice["claimPath"] != claim_path:
            raise ValueError


class Receiver(ThreadingHTTPServer):
    def __init__(self, port, fail_first):
        super().__init__(("127.0.0.1", port), Handler)
        self.fail_first = fail_first
        self.attempts = {}
        self.lock = threading.Lock()

    def record(self, notice):
        with self.lock:
            notification_id = notice["notificationId"]
            attempt = self.attempts.get(notification_id, 0) + 1
            self.attempts[notification_id] = attempt
            failed = self.fail_first and attempt == 1
            state = "SIMULATED_FAILURE" if failed else "ACKNOWLEDGED"
            print(json.dumps({"state": state, "attempt": attempt, **notice}), flush=True)
            return failed


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def do_POST(self):
        try:
            size = int(self.headers["Content-Length"])
            if not 0 < size <= 8192 or self.headers.get_content_type() != "application/json":
                raise ValueError
            notice = json.loads(self.rfile.read(size))
            validate_notice(notice, self.path)
        except (AttributeError, TypeError, ValueError):
            self.send_error(400)
            return
        if self.server.record(notice):
            self.send_error(503)
            return
        body = json.dumps({"notificationId": notice["notificationId"]}).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--port", type=int, default=0)
    parser.add_argument("--fail-first", action="store_true", help="return 503 on the first attempt for each notification ID")
    args = parser.parse_args()
    with Receiver(args.port, args.fail_first) as receiver:
        origin = f"http://127.0.0.1:{receiver.server_port}"
        print(json.dumps({"state": "READY", "weComURL": origin + "/wecom", "feishuURL": origin + "/feishu"}), flush=True)
        try:
            receiver.serve_forever()
        except KeyboardInterrupt:
            pass


if __name__ == "__main__":
    main()
