"""What does the official emulator decide for a Security Rules condition? (#61, #89)

Usage (from the repository root), with the jar started under an English locale
(`java -Duser.language=en -Duser.country=US -jar …`; under another locale every warning and
evaluation error comes back as a 500):
    python3 -I tools/oracle/rules_conditions.py 127.0.0.1:8086 conditions.json > out.json

`conditions.json` is a list of `{"name": ..., "condition": ...}`. For each one the script loads

    rules_version = '2';
    service cloud.firestore {
      match /databases/{database}/documents {
        match /rooms/{room} {
          allow read: if <condition>;
        }
      }
    }

through `PUT /emulator/v1/projects/rules:securityRules` and reads `rooms/r1` over REST with no
`Authorization` header (rules apply). The condition is on line 5 and starts at column 22.
`rooms/r1` is written once, as the owner, with the fields in `DOCUMENT`.

Each result has `outcome`:
- `allow`: the read succeeded (the condition was true)
- `false`: 403 with `false for 'get' @ L5` among its messages
- `error`: 403 with only `evaluation error at L5:22 for 'get' @ L5` (the condition raised an
  error; the position is the start of the condition, not of the failing part)
- `internal`: any other status, such as 500 `UNKNOWN` without a message
- `compile`: the PUT was refused; `message` holds the compiler's message

A 403 message lists each evaluation's result, comma-separated. A condition that reads
`resource` is evaluated twice and the first evaluation always errors (firebase-tools #6252,
`docs/parity-exceptions.md`), so `evaluation error …, false for …` means the condition was false.
"""

import http.client
import json
import sys

HOST = sys.argv[1] if len(sys.argv) > 1 else "127.0.0.1:8080"
PROJECT = "rules"
DB = f"projects/{PROJECT}/databases/(default)"
DOC = f"/v1/{DB}/documents/rooms/r1"

DOCUMENT = {
    "n": {"integerValue": "1"},
    "f": {"doubleValue": 1.5},
    "one_f": {"doubleValue": 1.0},
    "s": {"stringValue": "abc"},
    "b": {"booleanValue": True},
    "nul": {"nullValue": None},
    "arr": {"arrayValue": {"values": [{"integerValue": "1"}, {"integerValue": "2"}, {"integerValue": "3"}]}},
    "tags": {"arrayValue": {"values": [{"stringValue": "a"}, {"stringValue": "b"}]}},
    "m": {"mapValue": {"fields": {"x": {"integerValue": "1"}, "y": {"stringValue": "z"}}}},
    "ts": {"timestampValue": "2020-01-02T03:04:05.123Z"},
    "geo": {"geoPointValue": {"latitude": 10.0, "longitude": 20.0}},
    "ref": {"referenceValue": f"{DB}/documents/rooms/r2"},
    "by": {"bytesValue": "AQI="},
    "big": {"integerValue": "9223372036854775807"},
}

RULES = """rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /rooms/{room} {
      allow read: if %s;
    }
  }
}
"""


def request(method, path, body=None, headers=None):
    c = http.client.HTTPConnection(*HOST.split(":"), timeout=30)
    data = None if body is None else json.dumps(body)
    c.request(method, path, data, {"Content-Type": "application/json", **(headers or {})})
    res = c.getresponse()
    raw = res.read().decode()
    c.close()
    try:
        return res.status, json.loads(raw) if raw.strip() else None
    except ValueError:
        return res.status, raw


def message(body):
    if isinstance(body, dict) and isinstance(body.get("error"), dict):
        return body["error"].get("message", "").strip()
    return body if isinstance(body, str) else None


def main():
    conditions = json.load(open(sys.argv[2]))
    status, _ = request("PATCH", DOC, {"fields": DOCUMENT}, {"Authorization": "Bearer owner"})
    assert status == 200, status
    results = []
    for c in conditions:
        rules = {"rules": {"files": [{"name": "firestore.rules", "content": RULES % c["condition"]}]}}
        status, body = request("PUT", f"/emulator/v1/projects/{PROJECT}:securityRules", rules)
        r = {"name": c["name"], "condition": c["condition"]}
        if status != 200:
            r.update(outcome="compile", status=status, message=message(body))
        else:
            status, body = request("GET", DOC)
            msg = message(body)
            if status == 200:
                r.update(outcome="allow")
            elif status == 403:
                msgs = [m.strip() for m in msg.split(", ")]
                false = any(m.startswith("false for") for m in msgs)
                r.update(outcome="false" if false else "error", messages=msgs)
            else:
                r.update(outcome="internal", status=status, message=msg)
        results.append(r)
    # One result per line, so a diff shows which conditions changed.
    sys.stdout.reconfigure(encoding="utf-8")
    print("{")
    print(f' "document": {json.dumps(DOCUMENT)},')
    print(f' "rules": {json.dumps(RULES)},')
    print(' "results": [')
    print(",\n".join("  " + json.dumps(r, ensure_ascii=False) for r in results))
    print(" ]")
    print("}")


if __name__ == "__main__":
    main()
