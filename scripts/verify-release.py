#!/usr/bin/env python3
"""Verify an SSH-signed annotated tag and monotonic, consistent release version."""
import json
import os
from pathlib import Path
import re
import subprocess
import sys


def git(*args):
    return subprocess.check_output(['git', *args], text=True).strip()


def verify(ref, expected):
    if not re.fullmatch(r'refs/tags/v\d+\.\d+\.\d+', ref):
        raise ValueError('release must be a stable vMAJOR.MINOR.PATCH tag')
    if git('cat-file', '-t', ref) != 'tag':
        raise ValueError('release tag must be annotated and SSH signed')
    signers = str(Path('.github/release-signers').resolve())
    subprocess.run(['git', '-c', 'gpg.format=ssh', '-c',
                    f'gpg.ssh.allowedSignersFile={signers}', 'verify-tag', ref], check=True)
    commit = git('rev-parse', f'{ref}^{{commit}}')
    if commit != expected:
        raise ValueError('verified tag does not identify the workflow commit')
    subprocess.run(['git', 'merge-base', '--is-ancestor', commit,
                    'refs/remotes/origin/main'], check=True)
    version = ref.removeprefix('refs/tags/v')
    for file in ['build/config.yml', 'build/linux/nfpm/nfpm.yaml']:
        # Match the application version, not the build-config schema version.
        values = re.findall(r'^\s*version:\s*"([\d.]+)"', Path(file).read_text(), re.M)
        if not values or any(v != version for v in values):
            raise ValueError(f'{file} version differs from signed tag')
    for file in ['frontend/package.json', 'frontend/package-lock.json']:
        if json.loads(Path(file).read_text())['version'] != version:
            raise ValueError(f'{file} version differs from signed tag')
    if f'## [{version}]' not in Path('CHANGELOG.md').read_text():
        raise ValueError('release version missing from changelog')
    number = tuple(map(int, version.split('.')))
    # 1.5.0 is an independently established published floor, even if its ref
    # is removed upstream. Full checkout also checks every existing stable tag.
    previous = [(1, 5, 0)]
    for tag in git('tag', '--list', 'v*').splitlines():
        if tag != ref.removeprefix('refs/tags/') and re.fullmatch(r'v\d+\.\d+\.\d+', tag):
            previous.append(tuple(map(int, tag[1:].split('.'))))
    if number <= max(previous):
        raise ValueError('release version must exceed all earlier stable versions')
    print(f'Verified signed release {version} at {commit}')


if __name__ == '__main__':
    try:
        verify(os.environ['GITHUB_REF'], os.environ['GITHUB_SHA'])
    except (KeyError, ValueError, subprocess.CalledProcessError) as error:
        sys.exit(f'Release verification refused: {error}')
