"""Select validation jobs from Git paths; no third-party dependencies."""
import argparse
import json
import os
import subprocess


TARGETS = [
    dict(runner='windows-2025', goos='windows', asset_os='windows', goarch='amd64'),
    dict(runner='windows-11-arm', goos='windows', asset_os='windows', goarch='arm64'),
    dict(runner='ubuntu-24.04', goos='linux', asset_os='linux', goarch='amd64'),
    dict(runner='ubuntu-24.04-arm', goos='linux', asset_os='linux', goarch='arm64'),
    dict(runner='macos-15-intel', goos='darwin', asset_os='macos', goarch='amd64'),
    dict(runner='macos-15', goos='darwin', asset_os='macos', goarch='arm64'),
]


def classify(paths):
    result = dict(core=False, web=False, windows=False, linux=False, macos=False)
    for path in paths:
        if path.startswith('docs/') or path.lower().endswith('.md'):
            continue
        if path.startswith(('cmd/', 'internal/', 'scripts/smoke-', 'scripts/ci-plan', 'scripts/test-ci-plan', '.github/workflows/')) or path in ('go.mod', 'go.sum'):
            result['core'] = True
        if path.startswith(('web/', 'scripts/ci-plan', '.github/workflows/')):
            result['web'] = True
        common = path == 'scripts/release-package.go' or path.startswith('scripts/release-package_')
        for platform in ('windows', 'linux', 'macos'):
            if common or path.startswith((f'desktop/{platform}/', f'packaging/{platform}/', f'scripts/release-{platform}', f'scripts/test-{platform}')):
                result[platform] = True
    return result


def matrix(platform):
    if platform not in ('all', 'windows', 'linux', 'macos'):
        raise ValueError('Unsupported package platform')
    return {'include': [item for item in TARGETS if platform == 'all' or item['asset_os'] == platform]}


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--platform')
    parser.add_argument('--base')
    args = parser.parse_args()
    if args.platform:
        outputs = {'matrix': json.dumps(matrix(args.platform), separators=(',', ':'))}
    else:
        base = args.base
        if not base or set(base) == {'0'}:
            # Git's empty tree, computed instead of relying on SHA-1 repositories.
            base = subprocess.check_output(['git', 'hash-object', '-t', 'tree', '--stdin'], input=b'').decode().strip()
        paths = subprocess.check_output(['git', 'diff', '--name-only', '-z', base, 'HEAD']).decode().split('\0')
        outputs = {key: str(value).lower() for key, value in classify(paths).items()}
    with open(os.environ['GITHUB_OUTPUT'], 'a', encoding='utf-8') as output:
        for key, value in outputs.items():
            print(f'{key}={value}', file=output)
