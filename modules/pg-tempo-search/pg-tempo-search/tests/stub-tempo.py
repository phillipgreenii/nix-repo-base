"""Loopback stub of Tempo's GET /api/search for the pg-tempo-search tests.

usage: stub-tempo.py DATASET.json PORT_FILE REQUEST_LOG

DATASET.json is a list of traces:
  {"traceID": "...", "start": <epoch s>, "end": <epoch s>,
   "spans": [{"spanID": "...", "durationNanos": <int>, "attrs": {"k": "v" | 1}}]}

A request returns the traces overlapping [start, end), at most `limit` of
them, in Tempo's response shape. A request missing start or end gets HTTP 400,
so a helper that forgets them fails loudly. Every request's query string is
appended to REQUEST_LOG as one JSON object per line.
"""

import json
import os
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import parse_qs, urlparse


def attr(key, value):
    if isinstance(value, int):
        return {"key": key, "value": {"intValue": str(value)}}
    return {"key": key, "value": {"stringValue": value}}


def render(trace):
    spans = [
        {
            "spanID": s["spanID"],
            "startTimeUnixNano": str(trace["start"] * 10**9),
            "durationNanos": str(s["durationNanos"]),
            "attributes": [attr(k, v) for k, v in s.get("attrs", {}).items()],
        }
        for s in trace["spans"]
    ]
    return {
        "traceID": trace["traceID"],
        "rootServiceName": "stub",
        "rootTraceName": "stub.root",
        "startTimeUnixNano": str(trace["start"] * 10**9),
        "durationMs": (trace["end"] - trace["start"]) * 1000,
        "spanSet": {"spans": spans, "matched": len(spans)},
    }


def main():
    dataset_path, port_file, log_path = sys.argv[1:4]
    with open(dataset_path, encoding="utf-8") as f:
        dataset = json.load(f)

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            url = urlparse(self.path)
            query = {k: v[0] for k, v in parse_qs(url.query).items()}
            with open(log_path, "a", encoding="utf-8") as log:
                log.write(json.dumps({"path": url.path, "query": query}) + "\n")
            if url.path != "/api/search" or "start" not in query or "end" not in query:
                self.send_response(400)
                self.end_headers()
                self.wfile.write(b"start and end are required\n")
                return
            start, end = int(query["start"]), int(query["end"])
            limit = int(query.get("limit", "20"))
            hits = [t for t in dataset if t["start"] < end and t["end"] >= start]
            body = json.dumps({"traces": [render(t) for t in hits[:limit]]}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass

    server = HTTPServer(("127.0.0.1", 0), Handler)
    with open(port_file + ".tmp", "w", encoding="utf-8") as f:
        f.write(str(server.server_address[1]))
    # Publish the port atomically: the tests wait for this file to appear.
    os.rename(port_file + ".tmp", port_file)
    server.serve_forever()


if __name__ == "__main__":
    main()
