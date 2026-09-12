"""Static YAML checks only; not a Docker build or real Compose merge test.

PyYAML is a test dependency.
"""
from pathlib import Path
import yaml

ROOT = Path(__file__).resolve().parents[1]
base = yaml.safe_load((ROOT / 'compose.yaml').read_text())
assert set(base['services']) == {'browser'}
browser = base['services']['browser']
assert browser['build'] == '.'
assert 'depends_on' not in browser
assert 'volumes' not in browser and 'volumes' not in base
assert browser['environment']['BROWSER_PROXY_URL'] == '${BROWSER_PROXY_URL:-}'
assert all(str(p).startswith('127.0.0.1:') for p in browser['ports'])
assert browser['read_only'] and browser['env_file'] == '.env'
assert 'BROWSER_PROXY_URL=\n' in (ROOT / '.env.example').read_text()
assert not list(ROOT.glob('compose.override.*')), 'cache must not load automatically'
assert 'BROWSER_PROXY_URL' not in (ROOT / 'Dockerfile').read_text()
print('PASS static YAML: standalone has one local service, no cache dependency/volume; an existing S3 proxy remains optional.')
print('Not executed: Docker build, Compose config interpolation/merge, container startup.')
