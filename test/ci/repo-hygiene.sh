#!/usr/bin/env bash
# Repository hygiene checks. Run from anywhere; it works on the tracked files of this checkout.
#
# Files under dashboard/, test/oracle/ and any testdata/ directory are skipped by every rule
# (the dashboard has Arabic translations; the oracle and testdata hold recordings and fixtures).
#
#   a. no Arabic letters in Markdown or MDX
#   b. no Arabic letters in code comments: comment-only lines, and trailing comments after code
#      ("//" in Go, TS, JS, Java, Kotlin, Swift, Dart and proto; "#" in shell, YAML and Python;
#      "--" in SQL). A comment marker inside a quoted string is not a comment.
#   c. no local paths in any tracked text file: /Volumes/, /Users/ (except /Users/Shared/) and
#      /home/<name>/
#   d. nothing at the top level of docs/ except the allowed entries
#
# "Arabic letters" means these ranges: U+0600-06FF, U+0750-077F, U+08A0-08FF, U+FB50-FDFF,
# U+FE70-FEFF.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

python3 - <<'PY'
import os, re, subprocess, sys

RANGES = ((0x0600, 0x06FF), (0x0750, 0x077F), (0x08A0, 0x08FF), (0xFB50, 0xFDFF), (0xFE70, 0xFEFF))
ARABIC = re.compile('[' + ''.join(chr(a) + '-' + chr(b) for a, b in RANGES) + ']')
LOCAL_PATH = re.compile(r'/Volumes/|/Users/(?!Shared/)|/home/[A-Za-z0-9._-]+/')
SKIP_DIRS = ('dashboard/', 'test/oracle/')
SLASH = ('.go', '.ts', '.tsx', '.js', '.mjs', '.cjs', '.java', '.kt', '.swift', '.dart', '.proto')
HASH = ('.sh', '.yml', '.yaml', '.py')
DASH = ('.sql',)
ALLOWED_DOCS = {
    'README.md', 'getting-started.md', 'sdks.md', 'CHANGELOG.md', 'architecture.md',
    'benchmarks.md', 'testing.md', 'security.md', 'guides', 'reference', 'assets',
}
THIS = 'test/ci/repo-hygiene.sh'

files = subprocess.run(['git', 'ls-files', '-z'], check=True, capture_output=True).stdout.decode().split('\0')
files = [f for f in files if f and os.path.isfile(f)]
failures = []

def skipped(path):
    return path.startswith(SKIP_DIRS) or '/testdata/' in '/' + path

def read_lines(path):
    try:
        with open(path, 'rb') as fh:
            data = fh.read()
    except OSError:
        return None
    if b'\0' in data[:8192]:
        return None  # binary
    return data.decode('utf-8', errors='replace').split('\n')

def comment_start(line, marker):
    """Index of the first comment marker outside a quoted string, or -1."""
    quote = None
    i = 0
    while i < len(line):
        c = line[i]
        if quote:
            if c == '\\':
                i += 2
                continue
            if c == quote:
                quote = None
        elif c in '"\'`':
            quote = c
        elif line.startswith(marker, i):
            return i
        i += 1
    return -1

def arabic_in_comment(line, ext):
    if not ARABIC.search(line):
        return False
    if ext in SLASH:
        markers, starts = ('//', '/*'), ('//', '/*', '*')
    elif ext in HASH:
        markers, starts = ('#',), ('#',)
    else:
        markers, starts = ('--',), ('--',)
    if line.lstrip().startswith(starts):
        return True
    for m in markers:
        at = comment_start(line, m)
        if at >= 0 and ARABIC.search(line[at:]):
            return True
    return False

for path in files:
    if skipped(path):
        continue
    text = read_lines(path)
    if text is None:
        continue
    ext = os.path.splitext(path)[1]
    if ext in ('.md', '.mdx'):
        for n, line in enumerate(text, 1):
            if ARABIC.search(line):
                failures.append(f'(a) Arabic in Markdown: {path}:{n}')
    if ext in SLASH + HASH + DASH:
        for n, line in enumerate(text, 1):
            if arabic_in_comment(line, ext):
                failures.append(f'(b) Arabic in a comment: {path}:{n}')
    if path != THIS:
        for n, line in enumerate(text, 1):
            if LOCAL_PATH.search(line):
                failures.append(f'(c) local path: {path}:{n}')

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
