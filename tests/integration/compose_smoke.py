"""Static YAML checks only; not a Docker build or real Compose merge test.

PyYAML is a test dependency.
"""
from pathlib import Path
import yaml

ROOT = Path(__file__).resolve().parents[2]
base = yaml.safe_load((ROOT / 'compose.yaml').read_text())
assert set(base['services']) == {'browser'}
browser = base['services']['browser']
assert browser['build'] == '.'
assert 'depends_on' not in browser
assert browser['volumes'] == ['object-cache:/cache']
assert set(base['volumes']) == {'object-cache'}
assert browser['environment']['CACHE_DIR'] == '/cache'
assert all(str(p).startswith('127.0.0.1:') for p in browser['ports'])
assert browser['read_only'] and browser['env_file'] == '.env'
example = (ROOT / '.env.example').read_text()
assert 'SFTP_KEY=\n' in example and 'SFTP_HOST_KEY=\n' in example
assert not list(ROOT.glob('compose.override.*')), 'cache must not load automatically'
print('PASS static YAML: one application, persistent embedded cache, inline SFTP settings documented.')
print('Not executed: Docker build, Compose config interpolation/merge, container startup.')
