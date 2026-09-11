"""Static YAML checks only; not a Docker build or real Compose merge test.

Runtime code uses only the Go standard library. PyYAML is a test dependency.
"""
from pathlib import Path
import yaml

ROOT = Path(__file__).resolve().parents[1]
base = yaml.safe_load((ROOT / 'compose.yaml').read_text())
optional = yaml.safe_load((ROOT / 'compose.cache.yaml').read_text())
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
assert set(optional['services']) == {'browser', 'cache-proxy'}
link = optional['services']['browser']
assert link['environment']['BROWSER_PROXY_URL'] == 'http://cache-proxy:8080'
assert link['depends_on']['cache-proxy']['condition'] == 'service_healthy'
proxy = optional['services']['cache-proxy']
assert 'ports' not in proxy, 'cache must not bypass browser authentication'
assert proxy['volumes'] == ['object-cache:/cache']
assert 'object-cache' in optional['volumes']
assert proxy['environment']['SPA_MODE'] == 'false'
assert proxy['environment']['INDEX_DOCUMENT'] == ''
assert proxy['environment']['ERROR_PAGE_404'] == ''
assert proxy['environment']['ERROR_PAGES_JSON'] == '{}'
assert 'BROWSER_PROXY_URL' not in (ROOT / 'Dockerfile').read_text()
print('PASS static YAML: standalone has one local service, no cache dependency/volume; cache is an explicit opt-in overlay with no published cache port.')
print('Not executed: Docker build, Compose config interpolation/merge, container startup.')
