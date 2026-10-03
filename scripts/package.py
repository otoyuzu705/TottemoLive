"""Package a Wails desktop build and installation guide; write its SHA-256 digest."""
import argparse
import hashlib
from pathlib import Path
import shutil
import subprocess
import tempfile
import zipfile

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--target', required=True, choices=['windows-amd64', 'macos-amd64', 'macos-arm64'])
parser.add_argument('--root', type=Path, default=Path('.'))
args = parser.parse_args()
root = args.root.resolve()
windows = args.target.startswith('windows-')
name = 'TottemoLive.exe' if windows else 'TottemoLive.app'
source = root / 'build/bin' / name
instructions = root / 'docs/INSTALL.md'
if not source.exists():
    parser.error(f'build output missing: {source}')
if not instructions.is_file():
    parser.error(f'installation guide missing: {instructions}')
output = root / 'dist'
output.mkdir(exist_ok=True)
archive = output / f'TottemoLive-{args.target}.zip'
if windows:
    with zipfile.ZipFile(archive, 'w', compression=zipfile.ZIP_DEFLATED) as zipped:
        zipped.write(source, name)
        zipped.write(instructions, 'INSTALL.md')
else:
    # ditto preserves executable bits, symlinks and macOS bundle metadata.
    with tempfile.TemporaryDirectory() as staging:
        stage = Path(staging)
        shutil.copytree(source, stage / name, symlinks=True)
        shutil.copy2(instructions, stage / 'INSTALL.md')
        subprocess.run(['ditto', '-c', '-k', '--sequesterRsrc', str(stage), str(archive)], check=True)
with archive.open('rb') as stream:
    digest = hashlib.file_digest(stream, 'sha256').hexdigest()
archive.with_suffix('.zip.sha256').write_text(f'{digest}  {archive.name}\n', encoding='utf-8')
print(archive)
