"""Real Chromium against compiled mori. Run: python3 -m tests.e2e.http_e2e."""
from __future__ import annotations
from pathlib import Path
from http.server import ThreadingHTTPServer
import os
import shutil
import subprocess
import tempfile
import threading
import time
import urllib.request
import zipfile

from tests.fixtures import s3 as source

def main():
    from playwright.sync_api import sync_playwright, expect
    with tempfile.TemporaryDirectory(prefix='mori-e2e-') as tmp:
        binary = Path(tmp) / 'mori'
        subprocess.run(['go', 'build', '-o', str(binary), './cmd/mori'], cwd=source.ROOT, check=True)
        fixture = ThreadingHTTPServer(('127.0.0.1', 0), source.S3Fixture)
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
                    port = source.free_port()
                    base_url = f'http://127.0.0.1:{port}'
                    env = {k: v for k, v in os.environ.items() if not k.startswith(('BROWSER_', 'S3_'))}
                    env.update(BROWSER_LISTEN_ADDR=f'127.0.0.1:{port}', BROWSER_TITLE='Files', BROWSER_USERNAME='tester', BROWSER_PASSWORD='test-browser-password', BROWSER_PUBLIC='false', BROWSER_DOWNLOAD_MODE=download_mode, BROWSER_PREVIEW_MODE=preview_mode, BROWSER_PRESIGN_TTL='15m', BROWSER_LIST_TTL='0s', S3_ENDPOINT=fixture_url, S3_BUCKET='test-bucket', S3_PREFIX='public/', S3_REGION='ap-northeast-2', S3_FORCE_PATH_STYLE='true', S3_ACCESS_KEY_ID='TESTACCESS', S3_SECRET_ACCESS_KEY='test-secret-key', S3_SESSION_TOKEN='')
                    process = subprocess.Popen([str(binary)], cwd=tmp, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
                    try:
                        for _ in range(100):
                            try:
                                with urllib.request.urlopen(base_url + '/_mori/healthz', timeout=.2):
                                    break
                            except OSError:
                                time.sleep(.05)
                        else:
                            raise RuntimeError('mori did not start')
                        context = browser.new_context(viewport={'width': 1360, 'height': 900}, timezone_id='Asia/Seoul', accept_downloads=True, http_credentials={'username':'tester','password':'test-browser-password'})
                        page = context.new_page()
                        page.on('pageerror', lambda e: source.ERRORS.append(str(e)))
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
                        try:
                            with page.expect_download() as event:
                                page.locator('#download-zip').click()
                        except Exception:
                            print('ZIP download failed:', page.locator('#notice').inner_text(), source.ERRORS, source.EVENTS[-12:])
                            raise
                        archive = event.value
                        assert archive.failure() is None, archive.failure()
                        assert archive.suggested_filename == 'files.zip'
                        with zipfile.ZipFile(archive.path()) as z:
                            assert len(z.infolist()) == len(source.DATA)
                            assert z.read('documents/guide/deep/한글.txt') == source.DATA['public/documents/guide/deep/한글.txt']
                            assert z.getinfo('documents/guide/empty/').is_dir()
                            assert all(item.compress_type == zipfile.ZIP_STORED for item in z.infolist())
                            assert z.read('README.md') == source.DATA['public/README.md']
                            assert z.read('한글 +&.txt') == source.DATA['public/한글 +&.txt']
                            assert z.testzip() is None
                        expect(page.locator('#notice')).to_contain_text('브라우저에서 확인')
                        with page.expect_download() as event:
                            page.locator('tr[data-key="README.md"] .download').click()
                        individual = event.value
                        assert individual.failure() is None
                        assert Path(individual.path()).read_bytes() == source.DATA['public/README.md']
                        with page.expect_response(lambda response: '/_mori/api/preview?' in response.url) as event:
                            page.locator('tr[data-key="README.md"] .entry-link').click()
                        descriptor = event.value.json()
                        expect(page.locator('#preview')).to_be_visible()
                        expect(page.locator('.preview-code')).to_contain_text('Simple S3 file browser.')
                        assert descriptor['mode'] == preview_mode
                        if preview_mode == 'presigned':
                            assert descriptor['url'].startswith(fixture_url) and 'X-Amz-Signature=' in descriptor['url']
                        else:
                            assert descriptor['url'].startswith('/_mori/api/object?')
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
                        source.FAIL_LIST = True
                        page.locator('#refresh').click()
                        expect(page.locator('#error')).to_contain_text('S3 접근이 거부되었습니다.')
                        source.FAIL_LIST = False
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
    assert not source.ERRORS, source.ERRORS
    assert any(e['signed'] for e in source.EVENTS), 'No browser followed a presigned link'
    print('PASS signatures independently verified by botocore; no cross-origin Basic credential leak; no JavaScript errors')

if __name__ == "__main__":
    main()
