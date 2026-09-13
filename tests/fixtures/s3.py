"""Shared HTTP S3 fixture with independent botocore signature checks."""
from __future__ import annotations
from datetime import datetime, timezone
from pathlib import Path
from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse, parse_qs, urlencode, quote
from unittest.mock import patch
import hashlib
import hmac
import os
import socket
import subprocess
import tempfile
import threading
import time
import urllib.request
from xml.sax.saxutils import escape
from botocore.auth import S3SigV4QueryAuth
from botocore.awsrequest import AWSRequest
from botocore.credentials import Credentials

ROOT = Path(__file__).resolve().parents[2]
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
PAGE_SIZE = 2

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
            end = min(start + PAGE_SIZE, len(names))  # S3 may return fewer than MaxKeys.
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

_BUILD = []

def _binary():
    """Build mori once per process; several servers reuse the one binary."""
    if os.getenv('MORI_TEST_BINARY'):
        return Path(os.environ['MORI_TEST_BINARY'])
    if not _BUILD:
        directory = tempfile.mkdtemp(prefix='mori-build-')
        path = Path(directory) / 'mori'
        subprocess.run(['go', 'build', '-o', str(path), './cmd/mori'], cwd=ROOT, check=True)
        _BUILD.append(path)
    return _BUILD[0]

@contextmanager
def serve(data=None, page_size=None, **settings):
    """Build mori, run it against this fixture, and yield its base URL.

    Listings are rendered by the server now, so a browser check cannot stand up
    a page by stubbing fetch; it needs the real handler. Both the UI and the
    end-to-end suites start it the same way from here.
    """
    global DATA, PAGE_SIZE
    original_data, original_page = DATA, PAGE_SIZE
    if data is not None:
        DATA = data
    if page_size is not None:
        PAGE_SIZE = page_size
    with tempfile.TemporaryDirectory(prefix='mori-test-') as tmp:
        binary = _binary()
        fixture = ThreadingHTTPServer(('127.0.0.1', 0), S3Fixture)
        threading.Thread(target=fixture.serve_forever, daemon=True).start()
        port = free_port()
        base_url = f'http://127.0.0.1:{port}'
        env = {k: v for k, v in os.environ.items() if not k.startswith(('BROWSER_', 'S3_', 'CACHE_', 'SERVE_', 'AUTH_'))}
        env.update(
            BROWSER_LISTEN_ADDR=f'127.0.0.1:{port}', BROWSER_TITLE='Files', BROWSER_PUBLIC='true',
            BROWSER_LIST_TTL='0s', CACHE_MODE='off',
            S3_ENDPOINT=f'http://127.0.0.1:{fixture.server_port}', S3_BUCKET='test-bucket', S3_PREFIX='public/',
            S3_REGION='ap-northeast-2', S3_FORCE_PATH_STYLE='true', S3_ACCESS_KEY_ID='TESTACCESS',
            S3_SECRET_ACCESS_KEY='test-secret-key', S3_SESSION_TOKEN='',
        )
        env.update({k: str(v) for k, v in settings.items()})
        process = subprocess.Popen([str(binary)], cwd=tmp, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        try:
            for _ in range(200):
                try:
                    with urllib.request.urlopen(base_url + '/_mori/healthz', timeout=.2):
                        break
                except OSError:
                    time.sleep(.05)
            else:
                raise RuntimeError('mori did not start: ' + (process.stderr.read().decode() if process.stderr else ''))
            yield base_url
        finally:
            process.terminate()
            process.wait(timeout=10)
            fixture.shutdown()
            DATA, PAGE_SIZE = original_data, original_page
