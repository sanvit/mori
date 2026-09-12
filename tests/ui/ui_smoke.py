"""DOM/interaction checks with test-only fetch fixtures; no browser-network E2E.
Requires Playwright + Chromium. Run python3 -m tests.ui.ui_smoke.
"""
from pathlib import Path
import os
import shutil
from playwright.sync_api import sync_playwright, expect

root = Path(__file__).resolve().parents[2]
html = (root/'web/index.html').read_text().replace('<script src="/_mori/assets/app.js" defer></script>', '').replace('<script src="/_mori/assets/preview.js" defer></script>', '').replace('<link rel="stylesheet" href="/_mori/assets/preview.css">', '').replace('<link rel="icon" href="/_mori/assets/favicon.svg" type="image/svg+xml">', '')
html = html.replace('<link rel="stylesheet" href="/_mori/assets/styles.css">', '<style>'+(root/'web/styles.css').read_text()+'</style>')
errors = []
with sync_playwright() as p:
    options={'headless':True, 'args':['--no-sandbox']}
    executable=os.getenv('CHROMIUM_PATH') or shutil.which('chromium')
    if executable: options['executable_path']=executable
    browser=p.chromium.launch(**options)
    page=browser.new_page(viewport={'width':1360,'height':900},timezone_id='Asia/Seoul')
    page.on('pageerror',lambda e:errors.append(str(e)))
    page.set_content(html)
    page.evaluate("""() => {
      globalThis.mode = 'paged'; globalThis.calls = []; globalThis.downloads = [];
      HTMLAnchorElement.prototype.click = function() { downloads.push({href:this.getAttribute('href'), name:this.download}); };
      globalThis.fetch = async (url, options={}) => {
        calls.push({url, method:options.method || 'GET', body:options.body, headers:options.headers});
        if (url.startsWith('/_mori/api/config')) return new Response(JSON.stringify({title:'Files',zipMaxFiles:4,zipMaxBytes:1073741824,downloadMode:'presigned',previewMode:'proxy'}));
        if (url === '/_mori/api/archive') {
          if (mode === 'zip-error') return new Response(JSON.stringify({message:'선택한 파일의 합계가 서버의 ZIP 용량 제한을 넘었습니다.'}),{status:413});
          if (mode === 'zip-wait') return new Promise((resolve, reject) => {
            options.signal.addEventListener('abort', () => reject(new DOMException('Aborted','AbortError')), {once:true});
          });
          return new Response(JSON.stringify({url:'/_mori/api/archive?token='+'a'.repeat(43),filename:'files.zip',files:5}));
        }
        if (mode === 'error') return new Response(JSON.stringify({message:'S3 접근이 거부되었습니다.'}),{status:403});
        if (mode === 'empty') return new Response(JSON.stringify({entries:[],prefix:''}));
        const params = new URL(url,'https://test.invalid').searchParams;
        const prefix = params.get('prefix') || '';
        if (prefix === 'documents/') return new Response(JSON.stringify({entries:[{key:'documents/설치 & 설정.txt',name:'설치 & 설정.txt',size:40,folder:false,modified:''}],prefix}));
        const second = params.get('cursor') === 'second';
        const entries=[{key:'documents/',name:'documents',folder:true,size:0,modified:''},{key:'README.md',name:'README.md',folder:false,size:40,modified:'2026-09-09T06:00:00Z'}];
        if (second || mode === 'large') entries.push({key:'later-file.txt',name:'later-file.txt',folder:false,size:80,modified:''},{key:'<img src=x onerror=alert(1)>.txt',name:'<img src=x onerror=alert(1)>.txt',folder:false,size:90,modified:''});
        if (mode === 'large') entries.push({key:'fourth.txt',name:'fourth.txt',folder:false,size:1,modified:''});
        return new Response(JSON.stringify({entries,prefix:'',cursor:second||mode==='large'?'':'second'}));
      };
    }""")
    page.add_script_tag(content=(root/'web/app.js').read_text())
    expect(page.locator('.file-row')).to_have_count(1)
    assert page.locator('input[type=search], #filter, #demo-label, aside').count()==0
    assert '데모 파일' not in page.locator('body').inner_text()
    assert page.locator('.folder-row input').count()==1
    assert '하위' in page.locator('.folder-row input').get_attribute('title')
    expect(page.locator('#download-zip')).not_to_be_visible()
    page.locator('.file-row input').check()
    expect(page.locator('#download-zip')).to_have_text('ZIP 다운로드 (1)')
    before=page.evaluate('calls.length')
    page.locator('#load-more').click()
    expect(page.locator('.file-row')).to_have_count(3)
    assert page.evaluate('calls.length')==before+1
    assert page.locator('.file-row input:checked').count()==1
    assert page.locator('#select-all').evaluate('(el)=>el.indeterminate')
    page.locator('#select-all').check()
    expect(page.locator('#download-zip')).to_have_text('ZIP 다운로드 (4)')
    page.locator('[data-sort="size"]').click()
    assert page.locator('.file-row input:checked').count()==3
    sizes=page.locator('.file-row .size-cell').evaluate_all('(nodes)=>nodes.map(n=>parseInt(n.title))')
    assert sizes==sorted(sizes)
    page.locator('#download-zip').click()
    expect(page.locator('#notice')).to_contain_text('브라우저에서 확인')
    assert page.evaluate('downloads.length')==1
    post=page.evaluate("calls.find(c=>c.url==='/_mori/api/archive')")
    import json
    payload=json.loads(post['body'])
    assert post['method']=='POST' and post['headers']['X-Mori-Request']=='1'
    assert payload['prefix']=='' and len(payload['keys'])==4 and 'documents/' in payload['keys']
    assert set(payload)=={'prefix','keys'}, 'Client submitted trusted-looking sizes'
    assert page.evaluate('downloads[0].href').startswith('/_mori/api/archive?token=')
    assert not page.evaluate("calls.some(c=>c.url.startsWith('/_mori/api/archive?'))"), 'ZIP body incorrectly fetched with JS'
    page.evaluate("mode='zip-error'")
    page.locator('#download-zip').click()
    expect(page.locator('#notice')).to_contain_text('용량 제한')
    assert page.evaluate('downloads.length')==1
    page.evaluate("mode='zip-wait'")
    page.locator('#download-zip').click()
    expect(page.locator('#download-zip')).to_have_text('확인 중…')
    page.locator('.folder-row .entry-link').click()
    expect(page.locator('#breadcrumbs a').last).to_have_text('documents')
    expect(page.locator('#download-zip')).not_to_be_visible()
    assert page.evaluate('downloads.length')==1
    link=page.locator('.file-row .download').get_attribute('href')
    from urllib.parse import urlparse,parse_qs
    assert parse_qs(urlparse(link).query)=={'key':['documents/설치 & 설정.txt'],'download':['1']}
    assert page.locator('.file-row .entry-link').get_attribute('target')=='_blank'
    assert page.locator('.parent-row input').count()==0
    assert page.locator('.file-row .download').get_attribute('title')=='S3 직접 다운로드'
    page.evaluate("mode='large'")
    page.locator('.parent-row .entry-link').click()
    expect(page.locator('.file-row')).to_have_count(4)
    assert page.locator('tbody img').count()==0
    expect(page.locator('tbody')).to_contain_text('<img src=x onerror=alert(1)>.txt')
    page.locator('#select-all').click()
    expect(page.locator('#notice')).to_contain_text('최대 4개')
    assert page.locator('.file-row input:checked').count()==0
    page.locator('.folder-row input').check()
    for i in range(3): page.locator('.file-row input').nth(i).check()
    expect(page.locator('.file-row input').nth(3)).to_be_disabled()
    assert page.locator('.folder-row input:checked').count()==1
    if os.getenv('MORI_TEST_SCREENSHOTS'):
        page.screenshot(path=os.path.join(os.environ['MORI_TEST_SCREENSHOTS'], 'desktop.png'), full_page=True)
    page.set_viewport_size({'width':390,'height':844})
    if os.getenv('MORI_TEST_SCREENSHOTS'):
        page.screenshot(path=os.path.join(os.environ['MORI_TEST_SCREENSHOTS'], 'mobile.png'), full_page=True)
    assert not page.evaluate('document.documentElement.scrollWidth > innerWidth')
    page.evaluate("mode='error'")
    page.locator('#refresh').click()
    expect(page.locator('#error')).to_have_text('S3 접근이 거부되었습니다.')
    expect(page.locator('#refresh')).to_be_enabled()
    page.evaluate("mode='empty'")
    page.locator('#refresh').click()
    expect(page.locator('.message')).to_have_text('이 폴더는 비어 있습니다.')
    expect(page.locator('#select-all')).to_be_disabled()
    assert not errors,errors

    # BROWSER_ZIP_ENABLED=false: no selection column, toolbar, or archive calls.
    page=browser.new_page(viewport={'width':1360,'height':900},timezone_id='Asia/Seoul')
    page.on('pageerror',lambda e:errors.append(str(e)))
    page.set_content(html)
    page.evaluate("""() => {
      globalThis.calls = [];
      globalThis.fetch = async (url, options={}) => {
        calls.push({url, method:options.method || 'GET'});
        if (url.startsWith('/_mori/api/config')) return new Response(JSON.stringify({title:'Files',zipEnabled:false,zipMaxFiles:4,zipMaxBytes:1073741824,downloadMode:'proxy',previewMode:'proxy'}));
        return new Response(JSON.stringify({entries:[{key:'documents/',name:'documents',folder:true,size:0,modified:''},{key:'README.md',name:'README.md',folder:false,size:40,modified:'2026-09-09T06:00:00Z'}],prefix:''}));
      };
    }""")
    page.add_script_tag(content=(root/'web/app.js').read_text())
    expect(page.locator('.file-row')).to_have_count(1)
    expect(page.locator('#select-all')).to_have_count(0)
    assert page.locator('input[type=checkbox], col.select-col, .select-cell').count()==0
    expect(page.locator('#selection-tools')).not_to_be_visible()
    assert page.locator('thead th').count()==page.locator('tbody tr.file-row td').count()==4
    expect(page.locator('.file-row .download')).to_have_attribute('href', '/_mori/api/object?key=README.md&download=1')
    page.evaluate("""() => { globalThis.fetch = async () => new Response(JSON.stringify({entries:[],prefix:'empty/'})); location.hash = '#/empty/'; }""")
    expect(page.locator('.message')).to_have_text('이 폴더는 비어 있습니다.')
    assert page.locator('.message').get_attribute('colspan')=='4'
    assert not page.evaluate("calls.some(c => c.url.startsWith('/_mori/api/archive'))")
    page.set_viewport_size({'width':390,'height':844})
    assert not page.evaluate('document.documentElement.scrollWidth > innerWidth')
    assert not errors,errors
    browser.close()
print('PASS Chromium DOM fixtures: search-free UI, file/folder selection with recursive ZIP keys, paging/sort persistence, ZIP prepare/native-link initiation, server errors, preparation cancel, selection limit, safe names, navigation, mobile, empty state, ZIP disabled via config')
