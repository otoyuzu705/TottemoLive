"""Exercise release scripts against isolated files, without changing the checkout."""
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
import zipfile

SCRIPTS = Path(__file__).resolve().parent


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def run_script(self, name, *args):
        return subprocess.run(
            [sys.executable, str(SCRIPTS / name), *map(str, args)],
            cwd=self.root, capture_output=True, text=True,
        )

    def test_tag_updates_only_product_version(self):
        config = self.root / 'wails.json'
        config.write_text(json.dumps({'name': 'TottemoLive', 'info': {'productVersion': '1.1.0'}}))
        result = self.run_script('set-version.py', 'v2.3.4', '--config', config)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(config.read_text()), {'name': 'TottemoLive', 'info': {'productVersion': '2.3.4'}})

    def test_invalid_tags_do_not_modify_config(self):
        config = self.root / 'wails.json'
        original = '{"info":{"productVersion":"1.1.0"}}'
        for tag in ['1.2.3', 'v01.2.3', 'v1.2', 'v1.2.3;echo unsafe', 'v1.2.3-beta', 'v1.2.3\n']:
            with self.subTest(tag=tag):
                config.write_text(original)
                result = self.run_script('set-version.py', tag, '--config', config)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(config.read_text(), original)

    def test_windows_zip_contains_executable_instructions_and_checksum(self):
        binary = self.root / 'build/bin/TottemoLive.exe'
        binary.parent.mkdir(parents=True)
        binary.write_bytes(b'Windows fixture')
        instructions = self.root / 'docs/INSTALL.md'
        instructions.parent.mkdir()
        instructions.write_text('installation fixture')
        result = self.run_script('package.py', '--target', 'windows-amd64', '--root', self.root)
        self.assertEqual(result.returncode, 0, result.stderr)
        archive = self.root / 'dist/TottemoLive-windows-amd64.zip'
        with zipfile.ZipFile(archive) as zipped:
            self.assertEqual(set(zipped.namelist()), {'TottemoLive.exe', 'INSTALL.md'})
            self.assertEqual(zipped.read('TottemoLive.exe'), b'Windows fixture')
            self.assertEqual(zipped.read('INSTALL.md'), b'installation fixture')
        checksum = archive.with_suffix('.zip.sha256').read_text()
        self.assertEqual(checksum, hashlib.sha256(archive.read_bytes()).hexdigest() + '  ' + archive.name + '\n')

    def test_missing_binary_produces_no_archive(self):
        result = self.run_script('package.py', '--target', 'windows-amd64', '--root', self.root)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.root / 'dist/TottemoLive-windows-amd64.zip').exists())

    @unittest.skipUnless(sys.platform == 'darwin', 'ditto is a macOS tool')
    def test_mac_zip_preserves_app_layout_and_executable_permission(self):
        binary = self.root / 'build/bin/TottemoLive.app/Contents/MacOS/TottemoLive'
        binary.parent.mkdir(parents=True)
        binary.write_text('#!/bin/sh\nexit 0\n')
        binary.chmod(0o755)
        instructions = self.root / 'docs/INSTALL.md'
        instructions.parent.mkdir()
        instructions.write_text('installation fixture')
        result = self.run_script('package.py', '--target', 'macos-arm64', '--root', self.root)
        self.assertEqual(result.returncode, 0, result.stderr)
        archive = self.root / 'dist/TottemoLive-macos-arm64.zip'
        extracted = self.root / 'extracted'
        subprocess.run(['ditto', '-x', '-k', str(archive), str(extracted)], check=True)
        app_binary = extracted / 'TottemoLive.app/Contents/MacOS/TottemoLive'
        self.assertTrue(app_binary.stat().st_mode & 0o111)
        self.assertEqual((extracted / 'INSTALL.md').read_text(), 'installation fixture')
        subprocess.run([str(app_binary)], check=True)


if __name__ == '__main__':
    unittest.main()
