"""Compiled mori -> HTTP -> test-only S3 fixture. No browser, no real S3.
Presigned URLs are followed and checked independently by botocore at the fixture.
"""
import base64
import http.client
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import time
from urllib.parse import urlparse
import zipfile
from http.server import ThreadingHTTPServer
import http_e2e as fixture

AUTH = 'Basic ' + base64.b64encode(b'tester:test-browser-password').decode()

def request(url, method='GET', body=None, headers=None):
    u = urlparse(url)
    conn = http.client.HTTPConnection(u.hostname, u.port, timeout=10)
    try:
        conn.request(method, u.path + ('?' + u.query if u.query else ''), body=body, headers=headers or {})
        response = conn.getresponse()
        return response.status, dict(response.getheaders()), response.read()
    finally:
        conn.close()

with tempfile.TemporaryDirectory(prefix='mori-http-') as tmp:
    binary=Path(tmp)/'mori'
    subprocess.run(['go','build','-o',str(binary),'./cmd/mori'],cwd=fixture.ROOT,check=True)
    origin=ThreadingHTTPServer(('127.0.0.1',0),fixture.S3Fixture)
    threading.Thread(target=origin.serve_forever,daemon=True).start()
    try:
        for download_mode,preview_mode in [('proxy','proxy'),('presigned','proxy'),('proxy','presigned'),('presigned','presigned')]:
            port=fixture.free_port();base=f'http://127.0.0.1:{port}'
            env={k:v for k,v in os.environ.items() if not k.startswith(('BROWSER_','S3_'))}
            env.update(BROWSER_LISTEN_ADDR=f'127.0.0.1:{port}',BROWSER_USERNAME='tester',BROWSER_PASSWORD='test-browser-password',BROWSER_PUBLIC='false',BROWSER_DOWNLOAD_MODE=download_mode,BROWSER_PREVIEW_MODE=preview_mode,BROWSER_PROXY_URL='',S3_ENDPOINT=f'http://127.0.0.1:{origin.server_port}',S3_REGION='ap-northeast-2',S3_BUCKET='test-bucket',S3_PREFIX='public/',S3_FORCE_PATH_STYLE='true',S3_ACCESS_KEY_ID='TESTACCESS',S3_SECRET_ACCESS_KEY='test-secret-key')
            process=subprocess.Popen([str(binary)],cwd=tmp,env=env,stdout=subprocess.DEVNULL,stderr=subprocess.PIPE)
            try:
                for _ in range(100):
                    try:
                        if request(base+'/healthz')[0]==200: break
                    except OSError: time.sleep(.05)
                else: raise RuntimeError('mori failed to start')
                headers={'Authorization':AUTH}
                assert request(base+'/api/config')[0]==401
                status,_,body=request(base+'/api/config',headers=headers)
                assert status==200 and json.loads(body)['downloadMode']==download_mode
                assert json.loads(body)['mode']=='direct', 'standalone process unexpectedly requires a proxy'
                status,_,body=request(base+'/api/list',headers=headers)
                listing=json.loads(body)
                assert status==200 and len(listing['entries'])==2 and listing['cursor']
                before=len(fixture.EVENTS)
                status,h,again=request(base+'/api/list',headers=headers)
                assert status==200 and json.loads(again)==listing and h['X-Listing-Cache']=='HIT'
                assert len(fixture.EVENTS)==before, 'standalone listing cache did not avoid S3 request'
                for download in (False,True):
                    mode=download_mode if download else preview_mode
                    url=base+'/api/object?key=README.md'+('&download=1' if download else '')
                    status,h,body=request(url,headers=headers)
                    if mode=='presigned':
                        assert status==307 and body==b'' and h['Cache-Control']=='private, no-store'
                        status,h,body=request(h['Location'])  # No application auth forwarded.
                    assert status==200 and body==fixture.DATA['public/README.md']
                    if mode=='proxy': assert h['X-Cache']=='BYPASS'
                    assert h.get('Content-Disposition','').startswith('attachment' if download else 'inline')
                # Preview helper issues only a GetObject source; resolving it performs
                # no HEAD/list/body read, and following it verifies Range + SigV4.
                before=len(fixture.EVENTS)
                status,h,body=request(base+'/api/preview?key=README.md',headers=headers)
                preview=json.loads(body)
                assert status==200 and preview['kind']=='text' and preview['mode']==preview_mode
                assert h['Cache-Control']=='private, no-store' and len(fixture.EVENTS)==before
                url=preview['url'] if preview_mode=='presigned' else base+preview['url']
                status,h,body=request(url,headers={'Range':'bytes=0-5',**({} if preview_mode=='presigned' else headers)})
                assert status==206 and body==fixture.DATA['public/README.md'][:6]
                assert h.get('Content-Range','').startswith('bytes 0-5/')
                # HEAD and listing stay server-side even with both presign modes enabled.
                for suffix in ('', '&download=1'):
                    status,h,body=request(base+'/api/object?key=README.md'+suffix, method='HEAD', headers=headers)
                    assert status==200 and 'Location' not in h and body==b'' and h['X-Delivery-Mode']=='proxy'
                body=json.dumps({'prefix':'','keys':['README.md','한글 +&.txt']}).encode()
                status,_,reply=request(base+'/api/archive',method='POST',body=body,headers={**headers,'Content-Type':'application/json','X-Mori-Request':'1','Origin':base})
                assert status==200,reply
                ticket=json.loads(reply)
                status,h,archive=request(base+ticket['url'],headers=headers)
                assert status==200 and h['Content-Type']=='application/zip' and h['X-Delivery-Mode']=='proxy'
                with zipfile.ZipFile(io.BytesIO(archive)) as z:
                    assert z.namelist()==['README.md','한글 +&.txt']
                    assert z.read('한글 +&.txt')==fixture.DATA['public/한글 +&.txt']
                    assert all(f.compress_type==zipfile.ZIP_STORED for f in z.infolist())
                    assert z.testzip() is None
                assert request(base+ticket['url'],headers=headers)[0]==410
                before=len(fixture.EVENTS)
                status,_,reply=request(base+'/api/archive',method='POST',body=json.dumps({'prefix':'','keys':['documents/','README.md','documents/']}).encode(),headers={**headers,'Content-Type':'application/json','X-Mori-Request':'1','Origin':base})
                assert status==200,reply
                ticket=json.loads(reply)
                prep=fixture.EVENTS[before:]
                assert all(e['list'] or e['method']=='HEAD' for e in prep), 'ZIP preparation fetched file bodies'
                assert all(e['prefix']=='public/documents/' and e['delimiter']=='' and not e['signed'] for e in prep if e['list'])
                assert sum(e['list'] for e in prep)==2, prep
                status,h,archive=request(base+ticket['url'],headers=headers)
                assert status==200 and h['X-Delivery-Mode']=='proxy'
                expected={k.removeprefix('public/'):v for k,v in fixture.DATA.items() if k.startswith('public/documents/') or k=='public/README.md'}
                with zipfile.ZipFile(io.BytesIO(archive)) as z:
                    assert set(z.namelist())==set(expected),z.namelist()
                    assert all(z.read(k)==v for k,v in expected.items())
                    assert all(f.compress_type==zipfile.ZIP_STORED for f in z.infolist())
                    assert z.getinfo('documents/guide/empty/').is_dir() and z.testzip() is None
                print(f'PASS standalone compiled HTTP integration (no cache proxy): {download_mode=} {preview_mode=}, signed URL follow, server-side HEAD/list, recursive paginated ZIP paths/empty folders/Store/CRC/single-use')
            finally:
                process.terminate()
                try: _,stderr=process.communicate(timeout=5)
                except subprocess.TimeoutExpired: process.kill();_,stderr=process.communicate()
                assert process.returncode in (0,-15),stderr
    finally:
        origin.shutdown();origin.server_close()
assert not fixture.ERRORS,fixture.ERRORS
print('PASS standalone all four delivery combinations, in-process listing cache, recursive ZIP; signatures verified by botocore HTTP fixture')
