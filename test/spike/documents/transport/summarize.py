#!/usr/bin/env python3
"""Summarizes the spike server's record (record.jsonl): one block per client, listener and path,
with the headers the report asks for and the Authorization header's shape (never its value: the
server records shapes only).

    python3 -I test/spike/documents/transport/summarize.py .generated/record.jsonl
"""
import json
import sys

KEYS = ("content-type", "te", "user-agent", "x-goog-api-client", "google-cloud-resource-prefix",
        "x-goog-request-params", "x-goog-is-transaction", "grpc-timeout", "grpc-accept-encoding",
        "x-firebase-gmpid")


def client(h):
    s = (h.get("x-goog-api-client", "") + " " + h.get("user-agent", "")).lower()
    for marker, name in (("gl-go", "go"), ("gl-node", "node"), ("gl-java", "android"),
                         ("okhttp", "android"), ("gl-cpp", "ios"), ("gl-objc", "ios"),
                         ("gl-swift", "ios"), ("grpc-c++", "ios"), ("grpc-objc", "ios"),
                         ("go-http-client", "go"), ("node", "node")):
        if marker in s:
            return name
    return "?"


def main(path):
    seen = set()
    for line in open(path, encoding="utf-8"):
        e = json.loads(line)
        h = e.get("headers") or {}
        if e.get("blocked"):
            key = ("blocked", e["blocked"])
            if key not in seen:
                seen.add(key)
                print(f"BLOCKED by the guard proxy: {e['method']} {e['blocked']}")
            continue
        what = e.get("grant") or e.get("authorization") or "(no authorization)"
        key = (client(h), e["listener"], e["path"], what, h.get("x-goog-api-client"), h.get("user-agent"))
        if key in seen:
            continue
        seen.add(key)
        print(f"[{key[0]}] {e['listener']} {e['proto']} {e['method']} {e['path']}")
        print(f"    {'grant' if e.get('grant') else 'authorization'}: {what}")
        for k in KEYS:
            if k in h:
                print(f"    {k}: {h[k]}")
        if e.get("other_headers"):
            print(f"    other: {' '.join(e['other_headers'])}")


if __name__ == "__main__":
    main(sys.argv[1])
