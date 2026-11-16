#!/usr/bin/env python3
"""Exercise the actual desktop bridge policy in sandboxed Linux WebKitGTK.

Requires Go, GTK4/WebKitGTK6, dbus-run-session and xvfb-run. The fixture binds
only loopback, registers a harmless in-memory Probe service, and invokes no
container engine. Retains its disposable directories and logs for inspection.
"""

import argparse
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output-parent', type=Path, help='existing directory for a new disposable fixture root')
    args = parser.parse_args()
    if sys.platform != 'linux':
        raise SystemExit('This native fixture requires Linux GTK4/WebKitGTK6.')
    repository = Path(__file__).resolve().parent.parent
    sources = repository / 'scripts/fixtures/webview-boundary'
    go = shutil.which('go')
    if go is None:
        raise SystemExit('Go is required.')
    for executable in ('dbus-run-session', 'xvfb-run'):
        if shutil.which(executable) is None:
            raise SystemExit(f'{executable} is required.')
    work = Path(tempfile.mkdtemp(prefix='podder-webview-', dir=args.output_parent))
    print(f'Native boundary fixture: {work}', flush=True)
    build = work / 'build'
    build.mkdir()
    for name in ('go.mod', 'go.sum', 'webview_security.go', 'webview_security_dev.go', 'webview_security_production.go', 'webview_security_desktop.go', 'webview_security_server.go'):
        shutil.copyfile(repository / name, build / name)
    shutil.copyfile(repository / 'frontend/src/bootstrap-policy.js', build / 'bootstrap-policy.js')
    shutil.copyfile(sources / 'main.go.txt', build / 'main.go')
    shutil.copyfile(sources / 'scenario.js', build / 'scenario.js')
    env = os.environ.copy()
    env['GOCACHE'] = str(work / 'go-cache')
    for name, flags in (('production', ['-tags', 'production']), ('development', [])):
        subprocess.run([go, 'build', '-mod=readonly', '-buildvcs=false', *flags, '-o', str(work / name), '.'], cwd=build, env=env, check=True)
    logs = []
    for name, binary, development in (('production', 'production', False), ('development', 'development', True), ('production-env', 'production', True)):
        # WebKit/D-Bus Unix sockets have short path limits. Keep HOME/XDG/TMP
        # beneath a separate short, disposable root even if output-parent is long.
        runtime_root = Path(tempfile.mkdtemp(prefix='pv-'))
        print(f'{name} runtime fixture: {runtime_root}', flush=True)
        child_env = env.copy()
        for key, relative in (('HOME', 'home'), ('XDG_CONFIG_HOME', 'config'), ('XDG_DATA_HOME', 'data'), ('XDG_CACHE_HOME', 'cache'), ('XDG_RUNTIME_DIR', 'runtime'), ('TMPDIR', 'tmp')):
            directory = runtime_root / relative
            directory.mkdir(mode=0o700)
            child_env[key] = str(directory)
        child_env.pop('FRONTEND_DEVSERVER_URL', None)
        child_env.pop('WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS', None)
        child_env.pop('PODDER_DISABLE_WEBKIT_SANDBOX', None)
        child_env.update(HARNESS_DEV='1' if development else '0', WEBKIT_DISABLE_COMPOSITING_MODE='1', WEBKIT_DISABLE_DMABUF_RENDERER='1', GTK_A11Y='none', NO_AT_BRIDGE='1')
        log = work / f'{name}.log'
        with log.open('w') as output:
            subprocess.run(['dbus-run-session', '--', 'xvfb-run', '-a', str(work / binary)], cwd=runtime_root, env=child_env, stdout=output, stderr=subprocess.STDOUT, check=True, timeout=45)
        logs.append(str(log))
    subprocess.run([sys.executable, str(sources / 'verify.py'), *logs], check=True)


if __name__ == '__main__':
    main()
