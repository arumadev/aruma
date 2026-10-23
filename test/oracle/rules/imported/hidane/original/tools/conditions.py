"""Builds the conditions tools/oracle/rules_conditions.py sends to the official emulator.

Usage: python3 -I conditions.py > conditions.json   (from spikes/rules-eval)

- `rules/<n>`: each case of cases/rules.json, as written.
- `heldout/<n>`: each case of cases/rules-heldout.json, as written. These were recorded after
  the evaluators were written and were not used to change them.
- `cel/<file>/<section>/<name>`: each cel-spec case without bindings, as a check that the
  expression has the value cel-spec expects: `(e) == <literal> && (e) is <type>`. When cel-spec
  expects an error, the check is `(e) == (e)`, which errors when `e` does.
- `cel-self/...`: `(e) == (e)` for the cases that expect a value, to tell an expression Rules
  rejects from a literal in the check it rejects.
"""

import base64
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))


def lit(v):
    (k, x), = v.items()
    if k == "int":
        return "(-9223372036854775807 - 1)" if x == "-9223372036854775808" else x
    if k == "double":
        if isinstance(x, str):
            return {"NaN": "(0.0 / 0.0)", "Infinity": "(1.0 / 0.0)", "-Infinity": "(-1.0 / 0.0)"}[x]
        r = repr(float(x))
        return r if any(c in r for c in ".e") else r + ".0"
    if k == "string":
        out = ""
        for ch in x:
            o = ord(ch)
            if ch == "\\":
                out += "\\\\"
            elif ch == "'":
                out += "\\'"
            elif ch == "\n":
                out += "\\n"
            elif ch == "\r":
                out += "\\r"
            elif ch == "\t":
                out += "\\t"
            elif o < 0x20 or o == 0x7F:
                out += "\\x%02x" % o
            else:
                out += ch
        return "'" + out + "'"
    if k == "bytes":
        return "b'" + "".join("\\x%02x" % b for b in base64.b64decode(x)) + "'"
    if k == "bool":
        return "true" if x else "false"
    if k == "null":
        return "null"
    if k == "list":
        return "[" + ", ".join(lit(e) for e in x) + "]"
    if k == "map":
        return "{" + ", ".join(f"{lit(a)}: {lit(b)}" for a, b in x) + "}"
    raise ValueError(k)


TYPES = {"int": "int", "double": "float", "string": "string", "bool": "bool", "bytes": "bytes",
         "list": "list", "map": "map"}


def check(expr, expect):
    e = f"({expr})"
    (k, x), = expect.items()
    if k == "error":
        return f"{e} == {e}"
    if k == "double" and x == "NaN":
        return f"math.isNaN{e}"
    if k == "null":
        return f"{e} == null"
    return f"({e} == {lit(expect)}) && ({e} is {TYPES[k]})"


def main():
    out = []
    for i, c in enumerate(json.load(open(f"{HERE}/cases/rules.json"))):
        out.append({"name": f"rules/{i}", "condition": c["expr"]})
    for i, c in enumerate(json.load(open(f"{HERE}/cases/rules-heldout.json"))):
        out.append({"name": f"heldout/{i}", "condition": c["expr"]})
    for c in json.load(open(f"{HERE}/cases/cel-spec.json"))["cases"]:
        if c["bindings"]:
            continue
        name = f"{c['file']}/{c['section']}/{c['name']}"
        out.append({"name": f"cel/{name}", "condition": check(c["expr"], c["expect"])})
        if "error" not in c["expect"]:
            out.append({"name": f"cel-self/{name}", "condition": f"({c['expr']}) == ({c['expr']})"})
    sys.stdout.reconfigure(encoding="utf-8")
    json.dump(out, sys.stdout, indent=0, ensure_ascii=False)


if __name__ == "__main__":
    main()
