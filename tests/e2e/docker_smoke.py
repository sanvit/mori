"""Actual built image: read-only root, non-root cache volume, cache HIT, health.
Run after: docker build -t mori:unified-test .
Only creates/removes uniquely named disposable test containers and volumes.
"""
import json
import subprocess
import threading
import time
import urllib.request
import uuid
from http.server import ThreadingHTTPServer
from tests.fixtures import s3 as fixture

def docker(*args):
    return subprocess.check_output(['docker', *args], text=True).strip()

name = 'mori-unified-test-' + uuid.uuid4().hex[:12]
volume = name + '-cache'
origin = ThreadingHTTPServer(('0.0.0.0', 0), fixture.S3Fixture)
threading.Thread(target=origin.serve_forever, daemon=True).start()
docker('volume', 'create', volume)
try:
    docker('run', '-d', '--name', name, '--read-only', '--cap-drop=ALL',
           '--security-opt=no-new-privileges:true', '--add-host=host.docker.internal:host-gateway',
           '-p', '127.0.0.1::8080', '-v', volume+':/cache',
           '-e', 'S3_ENDPOINT=http://host.docker.internal:'+str(origin.server_port),
           '-e', 'S3_BUCKET=test-bucket', '-e', 'S3_PREFIX=public/',
           '-e', 'S3_ACCESS_KEY_ID=TESTACCESS', '-e', 'S3_SECRET_ACCESS_KEY=test-secret-key',
           '-e', 'BROWSER_PUBLIC=true', '-e', 'HEALTH_PATH=/ready',
           '--health-interval=1s', '--health-start-period=1s', 'mori:unified-test')
    info=json.loads(docker('inspect', name))[0]
    port=info['NetworkSettings']['Ports']['8080/tcp'][0]['HostPort']
    base='http://127.0.0.1:'+port
    def request(path):
        with urllib.request.urlopen(base+path, timeout=5) as r:
            return r.status, r.headers, r.read()
    for _ in range(60):
        try:
            if request('/ready')[0]==200: break
        except OSError: time.sleep(.1)
    else: raise AssertionError(docker('logs', name))
    assert b'/_mori/assets/app.js' in request('/')[2]
    status,headers,body=request('/README.md')
    assert status==200 and body==fixture.DATA['public/README.md'] and headers['X-Cache']=='MISS'
    count=len(fixture.EVENTS)
    status,headers,body=request('/_mori/api/object?key=README.md')
    assert status==200 and headers['X-Cache']=='HIT' and len(fixture.EVENTS)==count
    docker('exec', name, '/usr/local/bin/mori', 'healthcheck')
    assert docker('exec', name, 'id', '-u')=='10001'
    docker('restart', '--time', '5', name)
    info=json.loads(docker('inspect', name))[0]
    port=info['NetworkSettings']['Ports']['8080/tcp'][0]['HostPort']
    base='http://127.0.0.1:'+port
    for _ in range(60):
        try:
            if request('/ready')[0]==200: break
        except OSError: time.sleep(.1)
    else: raise AssertionError(docker('logs', name))
    status,headers,body=request('/README.md')
    assert status==200 and body==fixture.DATA['public/README.md'] and headers['X-Cache']=='HIT'
    assert any(e['method']=='HEAD' for e in fixture.EVENTS[count:]), 'restart skipped metadata validation'
    print('PASS actual image: non-root/read-only root, persistent disk MISS/HIT, custom healthcheck, restart revalidation/reuse')
finally:
    subprocess.run(['docker','rm','-f',name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    docker('volume','rm',volume)
    origin.shutdown()
