"""Browser interaction checks against a running mori and the shared S3 fixture.

The listing itself is rendered by the server, so these checks read the real
markup and exercise what the page adds on top of it: selection, ZIP handoff,
client-side ordering and paging. Only the archive endpoint is stubbed, because
its responses (limit refused, still preparing) are what the UI has to handle.
Run: python3 -m tests.ui.ui_smoke
"""
import json
import os
import shutil
from urllib.parse import urlparse, parse_qs, unquote

from playwright.sync_api import sync_playwright, expect

from tests.fixtures import s3 as source

ARCHIVE_STUB = """
globalThis.downloads = []; globalThis.calls = []; globalThis.zipMode = 'ok';
HTMLAnchorElement.prototype.click = function () {
  if ((this.getAttribute('href') || '').startsWith('/_mori/api/archive')) {
    downloads.push({ href: this.getAttribute('href'), name: this.download });
    return;
  }
  return Object.getPrototypeOf(HTMLAnchorElement.prototype).click.call(this);
};
const real = globalThis.fetch;
globalThis.fetch = async (url, options = {}) => {
  calls.push({ url: String(url), method: options.method || 'GET', body: options.body, headers: options.headers });
  if (String(url) === '/_mori/api/archive') {
    if (zipMode === 'error') return new Response(JSON.stringify({ message: '선택한 파일의 합계가 서버의 ZIP 용량 제한을 넘었습니다.' }), { status: 413 });
    if (zipMode === 'wait') return new Promise((resolve, reject) => {
      options.signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true });
    });
    return new Response(JSON.stringify({ url: '/_mori/api/archive?token=' + 'a'.repeat(43), filename: 'Files.zip', files: 5 }));
  }
  return real(url, options);
};
"""

def main():
    errors = []
    with sync_playwright() as playwright:
        options = {'headless': True, 'args': ['--no-sandbox']}
        executable = os.getenv('CHROMIUM_PATH') or shutil.which('chromium')
        if executable:
            options['executable_path'] = executable
        browser = playwright.chromium.launch(**options)

        with source.serve(BROWSER_ZIP_MAX_FILES='4', BROWSER_DOWNLOAD_MODE='presigned') as base:
            context = browser.new_context(viewport={'width': 1360, 'height': 900}, timezone_id='Asia/Seoul')
            context.add_init_script(ARCHIVE_STUB)
            page = context.new_page()
            page.on('pageerror', lambda e: errors.append(str(e)))
            page.goto(base)

            # The server sent a complete listing; nothing was fetched to build it.
            expect(page.locator('.folder-row')).to_have_count(1)
            expect(page.locator('.file-row')).to_have_count(1)
            assert page.evaluate("calls.filter(c => c.url.includes('/_mori/api/list')).length") == 0
            assert page.locator('input[type=search], #filter, #demo-label, aside').count() == 0
            assert '데모 파일' not in page.locator('body').inner_text()
            assert '하위' in page.locator('.folder-row input').get_attribute('title')
            expect(page.locator('#download-zip')).not_to_be_visible()

            # Paging appends the next server-rendered page and keeps the selection.
            page.locator('.file-row input').first.check()
            expect(page.locator('#download-zip')).to_have_text('ZIP 다운로드 (1)')
            page.locator('#load-more').click()
            expect(page.locator('.file-row')).to_have_count(3)
            assert page.locator('.file-row input:checked').count() == 1
            assert page.locator('#select-all').evaluate('(el)=>el.indeterminate')

            page.locator('#select-all').check()
            expect(page.locator('#download-zip')).to_have_text('ZIP 다운로드 (4)')
            page.locator('[data-sort="size"]').click()
            assert page.locator('.file-row input:checked').count() == 3
            sizes = page.locator('.file-row .size-cell').evaluate_all('(nodes)=>nodes.map(n=>parseInt(n.title))')
            assert sizes == sorted(sizes), sizes

            page.locator('#download-zip').click()
            expect(page.locator('#notice')).to_be_empty()
            assert page.evaluate('downloads.length') == 1
            post = page.evaluate("calls.find(c => c.url === '/_mori/api/archive')")
            payload = json.loads(post['body'])
            assert post['method'] == 'POST' and post['headers']['X-Mori-Request'] == '1'
            assert payload['prefix'] == '' and len(payload['keys']) == 4 and 'documents/' in payload['keys']
            assert set(payload) == {'prefix', 'keys'}, 'Client submitted trusted-looking sizes'
            assert page.evaluate('downloads[0].href').startswith('/_mori/api/archive?token=')
            assert not page.evaluate("calls.some(c => c.url.startsWith('/_mori/api/archive?'))"), 'ZIP body incorrectly fetched with JS'

            page.evaluate("zipMode = 'error'")
            page.locator('#download-zip').click()
            expect(page.locator('#notice')).to_contain_text('용량 제한')
            assert page.evaluate('downloads.length') == 1
            page.evaluate("zipMode = 'wait'")
            page.locator('#download-zip').click()
            expect(page.locator('#download-zip')).to_have_text('확인 중…')

            # Folder links are ordinary navigations; the new page arrives rendered.
            page.locator('.folder-row .entry-link').click()
            page.wait_for_url(base + '/documents/')
            expect(page.locator('#breadcrumbs a').last).to_have_text('documents')
            expect(page.locator('#download-zip')).not_to_be_visible()
            assert page.evaluate('downloads.length') == 0, 'navigation carried a stale ZIP handoff'
            link = page.locator('.file-row .download').first.get_attribute('href')
            parsed = urlparse(link)
            assert unquote(parsed.path).startswith('/documents/'), link
            assert parse_qs(parsed.query) == {'download': ['1']}, link
            assert page.locator('.file-row .entry-link').first.get_attribute('target') == '_blank'
            assert page.locator('.parent-row input').count() == 0
            assert page.locator('.file-row .download').first.get_attribute('title') == 'S3 직접 다운로드'

            # A hostile name is text in every cell, never markup.
            while page.locator('#load-more').count():
                page.locator('#load-more').click()
                page.wait_for_timeout(150)
            assert page.locator('tbody img').count() == 0
            expect(page.locator('tbody')).to_contain_text('<img src=x onerror=alert(1)>.txt')

            if os.getenv('MORI_TEST_SCREENSHOTS'):
                page.screenshot(path=os.path.join(os.environ['MORI_TEST_SCREENSHOTS'], 'desktop.png'), full_page=True)
            page.set_viewport_size({'width': 390, 'height': 844})
            if os.getenv('MORI_TEST_SCREENSHOTS'):
                page.screenshot(path=os.path.join(os.environ['MORI_TEST_SCREENSHOTS'], 'mobile.png'), full_page=True)
            assert not page.evaluate('document.documentElement.scrollWidth > innerWidth')

            # An empty folder and an upstream failure are rendered pages too.
            page.goto(base + '/documents/guide/empty/')
            expect(page.locator('.message')).to_have_text('이 폴더는 비어 있습니다.')
            expect(page.locator('#select-all')).to_be_disabled()
            source.FAIL_LIST = True
            try:
                page.goto(base + '/documents/')
                expect(page.locator('#error')).to_contain_text('접근이 거부되었습니다')
            finally:
                source.FAIL_LIST = False
            assert not errors, errors
            context.close()

        # Selecting past the server's file limit is refused before any request.
        with source.serve(BROWSER_ZIP_MAX_FILES='2') as limited:
            context = browser.new_context(viewport={'width': 1360, 'height': 900})
            context.add_init_script(ARCHIVE_STUB)
            page = context.new_page()
            page.on('pageerror', lambda e: errors.append(str(e)))
            page.goto(limited)
            while page.locator('#load-more').count():
                page.locator('#load-more').click()
                page.wait_for_timeout(150)
            page.locator('#select-all').click()
            expect(page.locator('#notice')).to_contain_text('최대 2개')
            assert page.locator('input[data-select-key]:checked').count() == 0
            page.locator('.folder-row input').check()
            page.locator('.file-row input').first.check()
            expect(page.locator('.file-row input').nth(1)).to_be_disabled()
            assert not page.evaluate("calls.some(c => c.url.startsWith('/_mori/api/archive'))")
            assert not errors, errors
            context.close()

        # BROWSER_ZIP_ENABLED=false: no selection column, toolbar, or archive calls.
        with source.serve(BROWSER_ZIP_ENABLED='false') as base:
            context = browser.new_context(viewport={'width': 1360, 'height': 900}, timezone_id='Asia/Seoul')
            context.add_init_script(ARCHIVE_STUB)
            page = context.new_page()
            page.on('pageerror', lambda e: errors.append(str(e)))
            page.goto(base)
            expect(page.locator('.folder-row')).to_have_count(1)
            expect(page.locator('#select-all')).to_have_count(0)
            assert page.locator('input[type=checkbox], col.select-col, .select-cell').count() == 0
            expect(page.locator('#selection-tools')).not_to_be_visible()
            assert page.locator('thead th').count() == 4
            expect(page.locator('.file-row .download').first).to_have_attribute('href', '/README.md?download=1')
            page.goto(base + '/documents/guide/empty/')
            expect(page.locator('.message')).to_have_text('이 폴더는 비어 있습니다.')
            assert page.locator('.message').get_attribute('colspan') == '4'
            assert not page.evaluate("calls.some(c => c.url.startsWith('/_mori/api/archive'))")
            page.set_viewport_size({'width': 390, 'height': 844})
            assert not page.evaluate('document.documentElement.scrollWidth > innerWidth')
            assert not errors, errors
            context.close()
        browser.close()
    print('PASS Chromium against running mori: server-rendered listing, selection with recursive ZIP keys, appended paging, client ordering, ZIP prepare/refuse/cancel, safe names, navigation, mobile, empty and failed folders, ZIP disabled')

if __name__ == '__main__':
    main()
