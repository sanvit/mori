"""Optional offline fixture generator. Requires botocore, not used by runtime."""
from datetime import datetime, timezone
from pathlib import Path
from unittest.mock import patch
from urllib.parse import urlencode, quote
import json
import botocore
from botocore.auth import S3SigV4QueryAuth
from botocore.awsrequest import AWSRequest
from botocore.credentials import Credentials

cases = []
for method, base, token in [
    ('GET', 'https://s3.ap-northeast-2.amazonaws.com/test-bucket/public/docs/%ED%95%9C%EA%B8%80%20%2B%26%25%23.txt', ''),
    ('GET', 'https://test-bucket.s3.ap-northeast-2.amazonaws.com/public/report.pdf', 'test-session+/='),
    ('HEAD', 'https://objects.example.test:9000/gateway/test-bucket/a%20b.txt', 'test-session+/='),
    ('GET', 'https://objects.example.test/gateway/test-bucket/a%252Fb.txt', ''),
]:
    query = {'response-content-disposition':'attachment; filename="a b.txt"','response-content-type':'text/plain; charset=utf-8', 'response-cache-control':'private, no-store'} if method == 'GET' else {}
    url = base + ('?' + urlencode(query, quote_via=quote) if query else '')
    req = AWSRequest(method=method, url=url)
    with patch('botocore.auth.get_current_datetime', return_value=datetime(2026,9,9,6,0,0,tzinfo=timezone.utc)):
        S3SigV4QueryAuth(Credentials('TESTACCESS','test-secret-key',token or None), 's3','ap-northeast-2',expires=900).add_auth(req)
    cases.append({'method':method,'url':url,'token':token,'signed_url':req.url})
path = Path(__file__).with_name('presign-fixtures.json')
path.write_text(json.dumps({'generator':'botocore '+botocore.__version__, 'cases':cases}, indent=2))
print(path)
