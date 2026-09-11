"""Real Chromium + compiled mori + test-only HTTP S3 fixture.
No sample mode in the runtime. Requires Playwright/Chromium and botocore.
Run: python3 tests/http_e2e.py
"""
from __future__ import annotations
from datetime import datetime, timezone
from pathlib import Path
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler
from urllib.parse import urlparse, parse_qs, urlencode, quote
from unittest.mock import patch
import hashlib
import hmac
import json
import os
import shutil
import socket
import subprocess
import tempfile
import threading
import time
import urllib.request
import zipfile
from xml.sax.saxutils import escape
from botocore.auth import S3SigV4QueryAuth
from botocore.awsrequest import AWSRequest
from botocore.credentials import Credentials

ROOT = Path(__file__).resolve().parents[1]
DATA = {
    'public/README.md': b'# mori\nSimple S3 file browser.\n',
    'public/z-later.txt': b'Loaded from the second metadata page.\n',
    'public/\ud55c\uae00 +&.txt': '한글 파일 내용\n'.encode(),
    'public/documents/guide.txt': b'Folder-local files only.\n',
    'public/documents/<img src=x onerror=alert(1)>.txt': b'Safe filename rendering.\n',
    'public/documents/guide/deep/한글.txt': 'nested contents\n'.encode(),
    'public/documents/guide/empty/': b'',
}
EVENTS = []
ERRORS = []
LOCK = threading.Lock()
FAIL_LIST = False

class S3Fixture(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'
    def log_message(self, *args):
        pass
    def respond(self, status, body=b'', content_type='text/plain', headers=None):
        headers = dict(headers or {})
        headers.setdefault('Accept-Ranges', 'bytes')
        origin = self.headers.get('Origin')
        if origin:
            headers.update({'Access-Control-Allow-Origin':origin, 'Vary':'Origin', 'Access-Control-Expose-Headers':'Accept-Ranges, Content-Range, Content-Length, ETag'})
        byte_range = self.headers.get('Range', '')
        if status == 200 and self.command == 'GET' and byte_range.startswith('bytes=') and not content_type.startswith('application/xml'):
            import re
            match = re.fullmatch(r'bytes=(\d+)-(\d*)', byte_range)
            if match:
                length = len(body)
                first = int(match[1]); last = min(int(match[2]) if match[2] else length-1, length-1)
                if first >= length:
                    status, body = 416, b''; headers['Content-Range'] = f'bytes */{length}'
                else:
                    body = body[first:last+1]; status = 206
                    headers['Content-Range'] = f'bytes {first}-{last}/{length}'
        self.send_response(status)
        self.send_header('Content-Type', content_type)
        self.send_header('Content-Length', str(len(body)))
        for key, value in (headers or {}).items():
            self.send_header(key, value)
        self.end_headers()
        if self.command != 'HEAD':
            self.wfile.write(body)
    def do_OPTIONS(self):
        self.respond(204, headers={'Access-Control-Allow-Methods':'GET', 'Access-Control-Allow-Headers':'Range, If-Range'})
    def do_HEAD(self):
        self.do_GET()
    def do_GET(self):
        from urllib.parse import unquote
        u = urlparse(self.path)
        q = parse_qs(u.query, keep_blank_values=True)
        if u.path == '/favicon.ico':
            self.respond(404); return
        with LOCK:
            EVENTS.append({'method': self.command, 'path': unquote(u.path), 'signed': 'X-Amz-Signature' in q, 'prefix':q.get('prefix',[''])[0], 'delimiter':q.get('delimiter',[''])[0], 'list':q.get('list-type')==['2']})
        if 'X-Amz-Signature' in q:
            # Independently reconstruct query signing with botocore. The fake S3
            # server rejects invalid signatures, rather than blindly serving links.
            query = {k: v[0] for k, v in q.items() if not k.startswith('X-Amz-')}
            base = 'http://' + self.headers['Host'] + u.path
            unsigned = base + ('?' + urlencode(query, quote_via=quote) if query else '')
            request = AWSRequest(method=self.command, url=unsigned)
            stamp = datetime.strptime(q['X-Amz-Date'][0], '%Y%m%dT%H%M%SZ').replace(tzinfo=timezone.utc)
            with LOCK, patch('botocore.auth.get_current_datetime', return_value=stamp):
                S3SigV4QueryAuth(Credentials('TESTACCESS', 'test-secret-key'), 's3', 'ap-northeast-2', expires=int(q['X-Amz-Expires'][0])).add_auth(request)
            expected = parse_qs(urlparse(request.url).query)['X-Amz-Signature'][0]
            if not hmac.compare_digest(expected, q['X-Amz-Signature'][0]):
                ERRORS.append('Invalid browser presign signature')
                self.respond(403, b'Invalid signature'); return
            if self.headers.get('Authorization', '').startswith('Basic '):
                ERRORS.append('Browser credentials leaked across origin redirect')
                self.respond(403, b'Unexpected authorization'); return
        elif not self.headers.get('Authorization', '').startswith('AWS4-HMAC-SHA256 '):
            ERRORS.append('Unsigned server-to-S3 request')
            self.respond(403, b'Unsigned'); return
        if q.get('list-type') == ['2']:
            if FAIL_LIST:
                self.respond(403, b'<Error><Code>AccessDenied</Code></Error>'); return
            assert q.get('delimiter') in (None, ['/']) and q.get('max-keys') == ['1000']
            assert 'X-Amz-Signature' not in q, 'ListObjectsV2 must never be presigned'
            prefix = q.get('prefix', [''])[0]
            grouped = {}
            for key in DATA:
                if not key.startswith(prefix):
                    continue
                rest = key[len(prefix):]
                if '/' in rest and q.get('delimiter') == ['/']:
                    grouped[prefix + rest.split('/')[0] + '/'] = True
                else:
                    grouped[key] = False
            names = sorted(grouped)
            start = int(q.get('continuation-token', ['0'])[0])
            end = min(start + 2, len(names))  # S3 may return fewer than MaxKeys.
            xml = '<ListBucketResult><EncodingType>url</EncodingType>'
            for key in names[start:end]:
                if grouped[key]:
                    xml += '<CommonPrefixes><Prefix>' + quote(key, safe='') + '</Prefix></CommonPrefixes>'
                else:
                    xml += '<Contents><Key>' + quote(key, safe='') + '</Key><Size>' + str(len(DATA[key])) + '</Size><ETag>&quot;' + hashlib.sha256(DATA[key]).hexdigest() + '&quot;</ETag><LastModified>2026-09-09T06:00:00Z</LastModified></Contents>'
            xml += '<IsTruncated>' + str(end < len(names)).lower() + '</IsTruncated>'
            if end < len(names):
                xml += '<NextContinuationToken>' + str(end) + '</NextContinuationToken>'
            xml += '</ListBucketResult>'
            self.respond(200, xml.encode(), 'application/xml'); return
        key = unquote(u.path).removeprefix('/test-bucket/')
        if key not in DATA:
            self.respond(404, b'<Error><Code>NoSuchKey</Code></Error>'); return
        body = DATA[key]
        etag = '"' + hashlib.sha256(body).hexdigest() + '"'
        if self.headers.get('If-Match') not in (None, etag):
            self.respond(412, b'<Error><Code>PreconditionFailed</Code></Error>'); return
        headers = {'ETag': etag, 'Last-Modified': 'Wed, 09 Sep 2026 06:00:00 GMT'}
        if 'response-content-disposition' in q:
            headers['Content-Disposition'] = q['response-content-disposition'][0]
        self.respond(200, body, q.get('response-content-type', ['text/plain'])[0], headers)

def free_port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]

def main():
    from playwright.sync_api import sync_playwright, expect
    global FAIL_LIST
    with tempfile.TemporaryDirectory(prefix='mori-e2e-') as tmp:
        binary = Path(tmp) / 'mori'
        subprocess.run(['go', 'build', '-o', str(binary), './cmd/mori'], cwd=ROOT, check=True)
        fixture = ThreadingHTTPServer(('127.0.0.1', 0), S3Fixture)
        thread = threading.Thread(target=fixture.serve_forever, daemon=True)
        thread.start()
        fixture_url = f'http://127.0.0.1:{fixture.server_port}'
        try:
            with sync_playwright() as playwright:
                executable = os.getenv('CHROMIUM_PATH') or shutil.which('chromium')
                options = {'headless': True, 'args': ['--no-sandbox']}
                if executable:
                    options['executable_path'] = executable
                browser = playwright.chromium.launch(**options)
                for download_mode, preview_mode in [('presigned', 'proxy'), ('proxy', 'presigned')]:
                    port = free_port()
                    base_url = f'http://127.0.0.1:{port}'
                    env = {k: v for k, v in os.environ.items() if not k.startswith(('BROWSER_', 'S3_'))}
                    env.update(BROWSER_LISTEN_ADDR=f'127.0.0.1:{port}', BROWSER_TITLE='Files', BROWSER_USERNAME='tester', BROWSER_PASSWORD='test-browser-password', BROWSER_PUBLIC='false', BROWSER_DOWNLOAD_MODE=download_mode, BROWSER_PREVIEW_MODE=preview_mode, BROWSER_PRESIGN_TTL='15m', BROWSER_PROXY_URL='', BROWSER_LIST_TTL='0s', S3_ENDPOINT=fixture_url, S3_BUCKET='test-bucket', S3_PREFIX='public/', S3_REGION='ap-northeast-2', S3_FORCE_PATH_STYLE='true', S3_ACCESS_KEY_ID='TESTACCESS', S3_SECRET_ACCESS_KEY='test-secret-key', S3_SESSION_TOKEN='')
                    process = subprocess.Popen([str(binary)], cwd=tmp, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
                    try:
                        for _ in range(100):
                            try:
                                with urllib.request.urlopen(base_url + '/healthz', timeout=.2):
                                    break
                            except OSError:
                                time.sleep(.05)
                        else:
                            raise RuntimeError('mori did not start')
                        context = browser.new_context(viewport={'width': 1360, 'height': 900}, timezone_id='Asia/Seoul', accept_downloads=True, http_credentials={'username':'tester','password':'test-browser-password'})
                        page = context.new_page()
                        page.on('pageerror', lambda e: ERRORS.append(str(e)))
                        page.goto(base_url)
                        expect(page.locator('.file-row')).to_have_count(1)
                        expect(page.locator('.folder-row')).to_have_count(1)
                        assert page.locator('input[type=search], #filter, #demo-label, aside').count() == 0
                        expect(page.locator('#load-more')).to_be_visible()
                        expect(page.locator('#download-zip')).not_to_be_visible()
                        page.locator('.file-row input').check()
                        expect(page.locator('#download-zip')).to_have_text('ZIP 다운로드 (1)')
                        page.locator('#load-more').click()
                        expect(page.locator('.file-row')).to_have_count(3)
                        assert page.locator('.file-row input:checked').count() == 1
                        assert page.locator('#select-all').evaluate('(el) => el.indeterminate')
                        page.locator('#select-all').check()
                        expect(page.locator('#download-zip')).to_have_text('ZIP 다운로드 (4)')
                        page.locator('[data-sort="size"]').click()
                        assert page.locator('.file-row input:checked').count() == 3
                        with page.expect_download() as event:
                            page.locator('#download-zip').click()
                        archive = event.value
                        assert archive.failure() is None, archive.failure()
                        assert archive.suggested_filename == 'files.zip'
                        with zipfile.ZipFile(archive.path()) as z:
                            assert len(z.infolist()) == len(DATA)
                            assert z.read('documents/guide/deep/한글.txt') == DATA['public/documents/guide/deep/한글.txt']
                            assert z.getinfo('documents/guide/empty/').is_dir()
                            assert all(item.compress_type == zipfile.ZIP_STORED for item in z.infolist())
                            assert z.read('README.md') == DATA['public/README.md']
                            assert z.read('한글 +&.txt') == DATA['public/한글 +&.txt']
                            assert z.testzip() is None
                        expect(page.locator('#notice')).to_contain_text('브라우저에서 확인')
                        with page.expect_download() as event:
                            page.locator('tr[data-key="README.md"] .download').click()
                        individual = event.value
                        assert individual.failure() is None
                        assert Path(individual.path()).read_bytes() == DATA['public/README.md']
                        with page.expect_response(lambda response: '/api/preview?' in response.url) as event:
                            page.locator('tr[data-key="README.md"] .entry-link').click()
                        source = event.value.json()
                        expect(page.locator('#preview')).to_be_visible()
                        expect(page.locator('.preview-code')).to_contain_text('Simple S3 file browser.')
                        assert source['mode'] == preview_mode
                        if preview_mode == 'presigned':
                            assert source['url'].startswith(fixture_url) and 'X-Amz-Signature=' in source['url']
                        else:
                            assert source['url'].startswith('/api/object?')
                        page.locator('#preview-close').click()
                        expect(page.locator('#preview')).not_to_be_visible()
                        page.locator('.folder-row .entry-link').click()
                        expect(page.locator('.file-row')).to_have_count(2)
                        expect(page.locator('#download-zip')).not_to_be_visible()
                        assert page.locator('tbody img').count() == 0
                        expect(page.locator('tbody')).to_contain_text('<img src=x onerror=alert(1)>.txt')
                        page.locator('.parent-row .entry-link').click()
                        expect(page.locator('.file-row')).to_have_count(1)
                        page.set_viewport_size({'width':390,'height':844})
                        page.locator('.file-row input').check()
                        assert not page.evaluate('document.documentElement.scrollWidth > innerWidth')
                        global_errors_before = len(ERRORS)
                        FAIL_LIST = True
                        page.locator('#refresh').click()
                        expect(page.locator('#error')).to_contain_text('S3 접근이 거부되었습니다.')
                        FAIL_LIST = False
                        page.locator('#refresh').click()
                        expect(page.locator('.file-row')).to_have_count(1)
                        context.close()
                        print(f'PASS HTTP browser E2E: ZIP Store/native download, {download_mode=} {preview_mode=}, selection/pagination/sorting/navigation/mobile/errors')
                    finally:
                        process.terminate()
                        try:
                            _, stderr = process.communicate(timeout=5)
                        except subprocess.TimeoutExpired:
                            process.kill(); _, stderr = process.communicate()
                        if process.returncode not in (0, -15):
                            raise RuntimeError(stderr.decode())
                browser.close()
        finally:
            fixture.shutdown(); fixture.server_close()
    assert not ERRORS, ERRORS
    assert any(e['signed'] for e in EVENTS), 'No browser followed a presigned link'
    print('PASS signatures independently verified by botocore; no cross-origin Basic credential leak; no JavaScript errors')

if __name__ == "__main__":
    main()
