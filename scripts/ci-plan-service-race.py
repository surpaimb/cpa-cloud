"""Independently authored CI partition for the complete default service race suite.

This enumerates the actual race-built Go test binary at the checked-out commit.
Every top-level Test, Fuzz seed, and Example belongs to exactly one fixed shard;
benchmarks are excluded because ordinary ``go test`` does not execute them.
"""

import argparse
import hashlib
import os
import re
import subprocess
import sys


DEFAULT_CASE = re.compile(r'(?:Test|Fuzz|Example)\S*\Z')
BENCHMARK = re.compile(r'Benchmark\S*\Z')


def parse_cases(output):
    cases = []
    for line in output.splitlines():
        line = line.strip()
        if not line or line.startswith(('ok ', '? ')):
            continue
        if BENCHMARK.fullmatch(line):
            continue
        if not DEFAULT_CASE.fullmatch(line):
            raise ValueError(f'unexpected Go -list output: {line!r}')
        cases.append(line)
    if not cases or len(cases) != len(set(cases)):
        raise ValueError('Go -list returned no default cases or duplicate names')
    return sorted(cases)


def partition(cases, count):
    if count < 2 or count > 16:
        raise ValueError('shard count must be between 2 and 16')
    if not cases or len(cases) != len(set(cases)):
        raise ValueError('case universe must be nonempty and unique')
    shards = [[] for _ in range(count)]
    for name in sorted(cases):
        bucket = int.from_bytes(hashlib.sha256(name.encode('utf-8')).digest()[:8], 'big') % count
        shards[bucket].append(name)
    if any(not shard for shard in shards):
        raise ValueError('every shard must contain a default case')
    flattened = [name for shard in shards for name in shard]
    if len(flattened) != len(set(flattened)) or set(flattened) != set(cases):
        raise ValueError('shards must be disjoint and cover the full universe')
    return shards


def exact_regex(cases):
    if not cases:
        raise ValueError('empty shard')
    return '^(' + '|'.join(re.escape(name) for name in cases) + ')$'


def go_list(pattern):
    result = subprocess.run(
        ['go', 'test', '-race', '-list', pattern, './internal/service'],
        capture_output=True, text=True, encoding='utf-8',
    )
    if result.returncode:
        raise RuntimeError(f'Go race case enumeration failed: {result.stderr[-4000:]}')
    return parse_cases(result.stdout)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--shard-index', type=int, required=True)
    parser.add_argument('--shard-count', type=int, required=True)
    args = parser.parse_args()
    if not 0 <= args.shard_index < args.shard_count:
        parser.error('shard index out of range')
    if os.environ.get('CGO_ENABLED') != '1':
        parser.error('CGO_ENABLED=1 is required for the actual race-built suite')
    expected = os.environ.get('EXPECTED_HEAD', '')
    actual = subprocess.check_output(['git', 'rev-parse', 'HEAD'], text=True).strip()
    if len(expected) != 40 or actual != expected:
        parser.error(f'checkout HEAD mismatch: expected {expected!r}, actual {actual!r}')
    if subprocess.check_output(['git', 'status', '--porcelain=v1'], text=True).strip():
        parser.error('service race checkout is not clean')
    cases = go_list('.')
    shards = partition(cases, args.shard_count)
    selected = shards[args.shard_index]
    regex = exact_regex(selected)
    if go_list(regex) != selected:
        parser.error('Go -list regex does not select exactly the assigned shard')
    digest = hashlib.sha256(('\n'.join(cases) + '\n').encode('utf-8')).hexdigest()
    print(
        f'service race coverage: head={actual} universe={len(cases)} '
        f'universe_sha256={digest} shards={[len(shard) for shard in shards]} '
        f'shard={args.shard_index} selected={len(selected)}',
        file=sys.stderr,
    )
    output = os.environ.get('GITHUB_OUTPUT')
    if not output:
        parser.error('GITHUB_OUTPUT is required')
    with open(output, 'a', encoding='utf-8') as destination:
        print(f'regex={regex}', file=destination)
        print(f'universe_sha256={digest}', file=destination)
        print(f'case_count={len(selected)}', file=destination)


if __name__ == '__main__':
    main()
