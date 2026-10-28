#!/usr/bin/env bash
# Repository hygiene checks. Run from anywhere; it works on the tracked files of this checkout.
#
#   a. no Arabic letters in Markdown outside dashboard/ and test/oracle/
#   b. no Arabic letters in comment-only lines of source files outside dashboard/, test/oracle/
#      and any testdata/ directory
#   c. no "/Volumes/" in any tracked text file
#   d. nothing at the top level of docs/ except the allowed entries
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

python3 - <<'PY'
import re, subprocess, sys

ARABIC = re.compile('[؀-ۿ]')
SOURCE = ('.go', '.ts', '.tsx', '.js', '.mjs', '.cjs', '.java', '.kt', '.swift', '.dart', '.sh')
COMMENT = ('//', '#', '/*', '*')
ALLOWED_DOCS = {
    'README.md', 'getting-started.md', 'sdks.md', 'CHANGELOG.md', 'architecture.md',
    'benchmarks.md', 'testing.md', 'security.md', 'guides', 'reference', 'assets',
}

files = subprocess.run(['git', 'ls-files', '-z'], check=True, capture_output=True).stdout.decode().split('\0')
import os
files = [f for f in files if f and os.path.exists(f)]
failures = []

def excluded(path, dirs):
    return any(path.startswith(d) for d in dirs)

def lines(path):
    try:
        with open(path, 'rb') as fh:
            data = fh.read()
    except OSError:
        return None
    if b'\0' in data[:8192]:
        return None  # binary
    return data.decode('utf-8', errors='replace').split('\n')

this_script = 'test/ci/repo-hygiene.sh'
for path in files:
    text = lines(path)
    if text is None:
        continue
    if path.endswith('.md') and not excluded(path, ('dashboard/', 'test/oracle/')):
        for n, line in enumerate(text, 1):
            if ARABIC.search(line):
                failures.append(f'(a) Arabic in Markdown: {path}:{n}')
    if path.endswith(SOURCE) and not excluded(path, ('dashboard/', 'test/oracle/')) and '/testdata/' not in '/' + path:
        for n, line in enumerate(text, 1):
            if line.lstrip().startswith(COMMENT) and ARABIC.search(line):
                failures.append(f'(b) Arabic in a comment: {path}:{n}')
    if path != this_script:
        for n, line in enumerate(text, 1):
            if '/Volumes/' in line:
                failures.append(f'(c) local path "/Volumes/": {path}:{n}')

top = set()
for path in files:
    if path.startswith('docs/'):
        top.add(path.split('/')[1])
for name in sorted(top - ALLOWED_DOCS):
    failures.append(f'(d) unexpected entry under docs/: {name}')

if failures:
    print('\n'.join(failures))
    print(f'\n{len(failures)} problem(s) found', file=sys.stderr)
    sys.exit(1)
print('repo hygiene: ok')
PY
