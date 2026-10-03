"""Set the release product version from a validated stable vMAJOR.MINOR.PATCH tag."""
import argparse
import json
from pathlib import Path
import re

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('tag')
parser.add_argument('--config', type=Path, default=Path('wails.json'))
args = parser.parse_args()
if not re.fullmatch(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)', args.tag):
    parser.error('tag must be a stable version such as v1.2.3')
config = json.loads(args.config.read_text(encoding='utf-8'))
config['info']['productVersion'] = args.tag[1:]
args.config.write_text(json.dumps(config, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
