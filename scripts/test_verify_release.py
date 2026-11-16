"""Offline release-gate regressions; tags/keys exist only in temporary fixture repos."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('verify_release', Path(__file__).with_name('verify-release.py'))
policy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(policy)


class ReleaseGateTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix='pdr3-signing-')
        self.original = os.getcwd()
        os.chdir(self.directory.name)
        self.addCleanup(self.directory.cleanup)
        self.addCleanup(os.chdir, self.original)
        self.run_git('init', '-q')
        self.run_git('config', 'user.name', 'Regression fixture')
        self.run_git('config', 'user.email', 'fixture@example.invalid')
        subprocess.run(['ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-f', 'key'], check=True)
        self.run_git('config', 'gpg.format', 'ssh')
        self.run_git('config', 'user.signingkey', str(Path('key').resolve()))
        Path('.github').mkdir()
        Path('.github/release-signers').write_text('fixture@example.invalid namespaces="git" ' + Path('key.pub').read_text())
        self.metadata('1.5.1')
        self.run_git('add', '.github', 'build', 'frontend', 'CHANGELOG.md')
        self.run_git('commit', '-qm', 'fixture')
        self.commit = self.run_git('rev-parse', 'HEAD')
        self.run_git('update-ref', 'refs/remotes/origin/main', self.commit)

    def run_git(self, *args):
        return subprocess.check_output(['git', *args], text=True, stderr=subprocess.PIPE).strip()

    def metadata(self, version):
        for name in ['build/config.yml', 'build/linux/nfpm/nfpm.yaml']:
            file = Path(name)
            file.parent.mkdir(parents=True, exist_ok=True)
            file.write_text(f'version: "{version}"\n')
        Path('frontend').mkdir(exist_ok=True)
        for name in ['frontend/package.json', 'frontend/package-lock.json']:
            Path(name).write_text(json.dumps({'version': version}))
        Path('CHANGELOG.md').write_text(f'## [{version}] - Unreleased\n')

    def signed(self, version='1.5.1'):
        self.run_git('tag', '-s', '-m', 'fixture', 'v' + version)

    def test_trusted_signature_and_matching_metadata(self):
        self.signed()
        policy.verify('refs/tags/v1.5.1', self.commit)

    def test_commit_checkout_preserves_signed_tag(self):
        self.signed()
        self.run_git('update-ref', 'refs/heads/main', self.commit)
        origin = str(Path.cwd())
        with tempfile.TemporaryDirectory(prefix='pdr3-checkout-') as clone:
            subprocess.run(['git', 'init', '-q', clone], check=True)
            subprocess.run(['git', '-C', clone, 'remote', 'add', 'origin', origin], check=True)
            subprocess.run(['git', '-C', clone, 'fetch', '-q', 'origin',
                            '+refs/heads/*:refs/remotes/origin/*',
                            '+refs/tags/*:refs/tags/*'], check=True)
            os.chdir(clone)
            try:
                self.run_git('checkout', '-q', '--detach', self.commit)
                policy.verify('refs/tags/v1.5.1', self.commit)
                # Reproduce the failed pinned-checkout fallback: a tag object
                # differs from the event commit, then commit:tag strips it.
                self.assertNotEqual(self.run_git('rev-parse', 'refs/tags/v1.5.1'), self.commit)
                self.run_git('fetch', '-q', '--no-tags', 'origin',
                             f'+{self.commit}:refs/tags/v1.5.1')
                self.assertEqual(self.run_git('cat-file', '-t', 'refs/tags/v1.5.1'), 'commit')
                with self.assertRaisesRegex(ValueError, 'annotated and SSH signed'):
                    policy.verify('refs/tags/v1.5.1', self.commit)
            finally:
                os.chdir(origin)

    def test_unsigned_and_lightweight_tags_refused(self):
        for mode in ['annotated', 'lightweight']:
            with self.subTest(mode=mode):
                self.run_git('tag', '-d', 'v1.5.1') if 'v1.5.1' in self.run_git('tag') else None
                args = ['tag', '-a', '-m', 'unsigned', 'v1.5.1'] if mode == 'annotated' else ['tag', 'v1.5.1']
                self.run_git(*args)
                with self.assertRaises((ValueError, subprocess.CalledProcessError)):
                    policy.verify('refs/tags/v1.5.1', self.commit)

    def test_unknown_signer_refused(self):
        self.signed()
        Path('.github/release-signers').write_text('# nobody trusted\n')
        with self.assertRaises(subprocess.CalledProcessError):
            policy.verify('refs/tags/v1.5.1', self.commit)

    def test_sha_and_metadata_mismatch_refused(self):
        self.signed()
        with self.assertRaises(ValueError):
            policy.verify('refs/tags/v1.5.1', '0' * 40)
        self.metadata('1.5.2')
        with self.assertRaises(ValueError):
            policy.verify('refs/tags/v1.5.1', self.commit)

    def test_published_floor_and_higher_existing_tag_refused(self):
        self.metadata('1.5.0')
        self.signed('1.5.0')
        with self.assertRaises(ValueError):
            policy.verify('refs/tags/v1.5.0', self.commit)
        self.metadata('1.5.1')
        self.signed()
        self.run_git('tag', 'v1.6.0')
        with self.assertRaises(ValueError):
            policy.verify('refs/tags/v1.5.1', self.commit)


if __name__ == '__main__':
    unittest.main()
