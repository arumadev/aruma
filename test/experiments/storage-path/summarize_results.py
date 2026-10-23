#!/usr/bin/env python3
"""Prints, per scenario, each operation's result and the requests the stub saw for it.

    summarize_results.py results.jsonl requests.jsonl [--paths]

results.jsonl: one {"scenario", "results": [{"op", "ok", "detail"}], "blocked": [...]} per line.
requests.jsonl: the stub's log, with {"marker": "<scenario>"} lines written before each scenario.
"""
import collections
import json
import sys


def main():
    results_path, log_path = sys.argv[1], sys.argv[2]
    show_paths = "--paths" in sys.argv
    by_scenario = collections.defaultdict(list)
    current = None
    with open(log_path) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            e = json.loads(line)
            if "marker" in e:
                current = e["marker"]
                continue
            by_scenario[current].append(e)
    with open(results_path) as f:
        for line in f:
            line = line.strip()
            if not line.startswith("{"):
                continue
            r = json.loads(line)
            s = r["scenario"]
            ok = sum(1 for x in r["results"] if x["ok"])
            print(f"\n## {s}: {ok}/{len(r['results'])} ok")
            for x in r["results"]:
                print(f"  {'PASS' if x['ok'] else 'FAIL'} {x['op']:<22} {x.get('detail', '')}")
            for b in r.get("blocked", []) or []:
                print(f"  BLOCKED (guard) {b}")
            reqs = by_scenario.get(s, [])
            counts = collections.Counter()
            for e in reqs:
                where = e["listener"] + (" /" + e["prefix"] if e.get("prefix") else "")
                if e["op"] == "guard.block":
                    where += " " + e["path"]
                auth = e["auth"].split("(")[0]
                key = (e["op"], where, auth, e["status"])
                if show_paths:
                    key += (e["method"] + " " + e["path"].split("?")[0],)
                counts[key] += 1
            if counts:
                print("  stub saw:")
                for k, n in sorted(counts.items()):
                    print("    " + "  ".join(str(p) for p in k) + f"  x{n}")


if __name__ == "__main__":
    main()
