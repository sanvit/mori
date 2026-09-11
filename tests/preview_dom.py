"""Preview and mobile DOM tests; no browser-network / real-library integration.

Application JS is evaluated in an isolated about:blank document. HTTP responses,
image transport, and the PDF.js API are explicit TEST DOUBLES. Media Chrome is
unavailable in this test, so only its native-control fallback is exercised.
No double is shipped in web/ or used by the application at runtime.
Run: python3 tests/preview_dom.py
MORI_TEST_SCREENSHOTS=/path saves list/image-only fixture screenshots.
"""
import os
from pathlib import Path
import shutil
from playwright.sync_api import sync_playwright, expect
ROOT=Path(__file__).resolve().parents[1]
html=(ROOT/'web/index.html').read_text()
for script in ('app.js','preview.js'):
    html=html.replace(f'<script src="/{script}" defer></script>','')
for css in ('styles.css','preview.css'):
    html=html.replace(f'<link rel="stylesheet" href="/{css}">','<style>'+(ROOT/'web'/css).read_text()+'</style>')
html=html.replace('<link rel="icon" href="/favicon.svg" type="image/svg+xml">','')
fixtures=r'''() => {
  window.calls=[]; window.textCancelled=0; window.pdfDestroyed=0; window.pdfCancelled=0; window.pdfOptions=[];
  const names=[['Archive/',0,true],['Photos/',0,true],['01-mountains.jpg',1536000],['02-film.mp4',35900000],['03-soundtrack.mp3',4567000],['04-guide.pdf',243000],['05-README.md',512],['06-large.log',6*1024*1024],['07-locked.pdf',1200],['08-package.zip',2300000]];
  window.entries=names.map(([name,size,folder])=>({key:name,name,folder:!!folder,size,modified:'2026-09-09T02:30:00Z',etag:'test'}));
  const canvas=document.createElement('canvas'); canvas.width=960; canvas.height=640;
  const ctx=canvas.getContext('2d'),sky=ctx.createLinearGradient(0,0,0,640); sky.addColorStop(0,'#b7d1e3'); sky.addColorStop(1,'#eff2ed'); ctx.fillStyle=sky; ctx.fillRect(0,0,960,640);
  ctx.fillStyle='#d9e5e5'; ctx.beginPath();ctx.arc(710,160,62,0,Math.PI*2);ctx.fill();
  ctx.fillStyle='#809caf';ctx.beginPath();ctx.moveTo(0,480);ctx.lineTo(260,195);ctx.lineTo(490,460);ctx.lineTo(710,260);ctx.lineTo(960,510);ctx.lineTo(960,640);ctx.lineTo(0,640);ctx.fill();
  ctx.fillStyle='#aec3cb';ctx.beginPath();ctx.moveTo(196,268);ctx.lineTo(260,195);ctx.lineTo(338,287);ctx.lineTo(271,261);ctx.lineTo(236,281);ctx.fill();
  ctx.fillStyle='#4b6e7c';ctx.beginPath();ctx.moveTo(0,550);ctx.bezierCurveTo(210,350,360,700,960,430);ctx.lineTo(960,640);ctx.lineTo(0,640);ctx.fill();
  const testImage=canvas.toDataURL('image/png');
  const src=Object.getOwnPropertyDescriptor(HTMLImageElement.prototype,'src');
  Object.defineProperty(HTMLImageElement.prototype,'src',{...src,set(v){calls.push({imageSource:v});return src.set.call(this,v.startsWith('https://fixture.invalid/')?testImage:v);}});
  // Native audio/video requests are not a playback test. Prevent network requests.
  const msrc=Object.getOwnPropertyDescriptor(HTMLMediaElement.prototype,'src');
  Object.defineProperty(HTMLMediaElement.prototype,'src',{...msrc,set(v){this.dataset.testSource=v;}});
  HTMLMediaElement.prototype.load=function(){ this.dataset.released='true'; };
  HTMLMediaElement.prototype.pause=function(){ this.dataset.pausedByCleanup='true'; };
  window.fetch=async (value,opts={})=>{
    const url=String(value), u=new URL(url,'https://fixture.invalid');
    calls.push({url,credentials:opts.credentials,range:opts.headers?.Range,signal:opts.signal});
    if(u.pathname==='/api/config') return new Response(JSON.stringify({title:'Files',zipMaxFiles:200,zipMaxBytes:21474836480,downloadMode:'proxy',previewMode:'presigned'}));
    if(u.pathname==='/api/list') return new Response(JSON.stringify({entries:entries,cursor:''}));
    if(u.pathname==='/api/preview') {
      const key=u.searchParams.get('key');
      return new Response(JSON.stringify({name:key,kind:MoriPreview.kindOf(key),mode:'presigned',url:'https://fixture.invalid/object?'+new URLSearchParams({key}),textLimit:1048576}));
    }
    if(u.pathname==='/object') {
      const key=u.searchParams.get('key');
      if(key.includes('large')) {
        let count=0;
        return new Response(new ReadableStream({pull(c){c.enqueue(new TextEncoder().encode('x'.repeat(256*1024)));if(++count===8)c.close();},cancel(){textCancelled++;}}, {highWaterMark:0}),{headers:{'Content-Length':String(6*1024*1024)}});
      }
      return new Response('# Hello, mori\n\nA simple place for your files.\n\n<img src=x onerror="alert(1)">\n<script>globalThis.unsafeExecuted=true</script>\n');
    }
    throw new Error('Unexpected fixture URL '+url);
  };
  window.PDF_TEST_DOUBLE={
    GlobalWorkerOptions:{},PasswordResponses:{INCORRECT_PASSWORD:2},
    getDocument(options){
      pdfOptions.push(options);
      let resolveDoc,rejectDoc;
      const task={promise:new Promise((resolve,reject)=>{resolveDoc=resolve;rejectDoc=reject;}),destroy(){pdfDestroyed++;rejectDoc(new Error('destroyed'));return Promise.resolve();}};
      const doc={numPages:3,async getPage(number){
        await new Promise(r=>setTimeout(r,5));
        return {cleanup(){},getViewport({scale}){return {width:595*scale,height:842*scale};},getTextContent:async()=>({items:[{str:'Fixture page '+number}]}),render({canvasContext,viewport}){
          let reject,timeout;
          const promise=new Promise((resolve,r)=>{reject=r;timeout=setTimeout(()=>{canvasContext.fillStyle='white';canvasContext.fillRect(0,0,viewport.width,viewport.height);resolve();},20);});
          return {promise,cancel(){pdfCancelled++;clearTimeout(timeout);reject(Object.assign(new Error('cancelled'),{name:'RenderingCancelledException'}));}};
        }};
      }};
      setTimeout(()=>{
        if(options.url.includes('locked')) task.onPassword(pass=>{if(pass==='secret') resolveDoc(doc);else task.onPassword(pass2=>{if(pass2==='secret')resolveDoc(doc);},2);},1);
        else resolveDoc(doc);
      },10);
      return task;
    }
  };
}'''
errors=[]
with sync_playwright() as p:
    opts={'headless':True,'args':['--no-sandbox']}
    executable=os.getenv('CHROMIUM_PATH') or shutil.which('chromium')
    if executable: opts['executable_path']=executable
    browser=p.chromium.launch(**opts)
    page=browser.new_page(viewport={'width':1360,'height':900}, timezone_id='Asia/Seoul')
    page.on('pageerror',lambda e:errors.append(str(e)))
    page.set_content(html)
    page.evaluate(fixtures)
    source=(ROOT/'web/preview.js').read_text()
    # Test-only injection at the module boundary; production file remains untouched.
    source=source.replace('import(LIB.pdf)','Promise.resolve(window.PDF_TEST_DOUBLE)')
    source=source.replace('import(LIB.media)',"Promise.reject(new Error('Test intentionally does not load Media Chrome'))")
    page.add_script_tag(content=source)
    page.add_script_tag(content=(ROOT/'web/app.js').read_text())
    expect(page.locator('tr.file-row')).to_have_count(8)
    assert page.locator('input[type=search]').count()==0
    def entry(name): return page.locator('tr.file-row').filter(has=page.locator('.entry-name',has_text=name)).locator('.entry-link')
    def open_file(name): entry(name).click(); expect(page.locator('#preview')).to_be_visible()
    def close():
        page.locator('#preview-close').click()
        expect(page.locator('#preview')).not_to_be_visible()
        page.wait_for_timeout(60)
    def shot(name):
        dest=os.getenv('MORI_TEST_SCREENSHOTS')
        if dest:
            Path(dest).mkdir(parents=True,exist_ok=True)
            page.screenshot(path=str(Path(dest)/name),full_page=True)
    shot('desktop-list.png')
    for width in (320,360,390,768):
        page.set_viewport_size({'width':width,'height':844})
        assert not page.evaluate('document.documentElement.scrollWidth > innerWidth'),width
        if width<740:
            expect(page.locator('#mobile-sort')).to_be_visible()
            box=page.locator('.file-row .download').first.bounding_box(); assert box['width']>=44 and box['height']>=44,box
    page.set_viewport_size({'width':390,'height':844})
    shot('mobile-list.png')
    page.locator('.file-row input').first.check()
    expect(page.locator('#selection-tools')).to_be_visible()
    assert page.locator('#selection-tools').bounding_box()['y']>700
    shot('mobile-selection.png')
    page.locator('#clear-selection').click()
    expect(page.locator('#selection-tools')).not_to_be_visible()
    open_file('01-mountains.jpg')
    expect(page.locator('#preview-hint')).to_have_text('960 × 640')
    expect(page.locator('#preview-close')).to_be_focused()
    box=page.locator('#preview').bounding_box(); assert box['width']==390 and box['height']==844,box
    shot('mobile-image.png')
    page.locator('.image-tools [aria-label="확대"]').click()
    expect(page.locator('.zoom-label')).to_have_text('125%')
    page.locator('.image-tools [aria-label="화면에 맞춤"]').click()
    expect(page.locator('.zoom-label')).to_have_text('화면에 맞춤')
    page.set_viewport_size({'width':1360,'height':900})
    shot('desktop-image.png')
    page.evaluate('window.lastImage=document.querySelector(".preview-image")')
    page.keyboard.press('Escape')
    expect(page.locator('#preview')).not_to_be_visible()
    page.wait_for_timeout(60)
    assert page.evaluate('lastImage.getAttribute("src")') is None
    expect(entry('01-mountains.jpg')).to_be_focused()
    open_file('05-README.md')
    expect(page.locator('.preview-code')).to_contain_text('<img src=x')
    assert page.locator('.preview-code img, .preview-code script').count()==0
    assert not page.evaluate('!!window.unsafeExecuted')
    page.locator('.text-toggle').click(); expect(page.locator('.preview-code')).to_have_class('preview-code wrap')
    assert page.evaluate('calls.filter(c=>c.url?.includes("/object?")).every(c=>c.credentials==="omit")')
    close()
    open_file('06-large.log')
    expect(page.locator('.text-tools')).to_contain_text('처음 1 MiB')
    assert page.locator('.preview-code').evaluate('(n)=>n.textContent.length')==1048576
    assert page.evaluate('textCancelled')==1
    close()
    open_file('02-film.mp4')
    expect(page.locator('#preview-hint')).to_contain_text('기본 플레이어')
    assert page.locator('video').evaluate('(v)=>v.controls && v.preload === "metadata" && v.hasAttribute("playsinline") && !v.autoplay')
    page.evaluate('window.lastMedia=document.querySelector("video")')
    close()
    assert page.evaluate('lastMedia.dataset.pausedByCleanup === "true" && lastMedia.dataset.released === "true"')
    open_file('03-soundtrack.mp3')
    expect(page.locator('#preview-hint')).to_contain_text('기본 플레이어')
    expect(page.locator('audio')).to_be_visible()
    close()
    open_file('04-guide.pdf')
    expect(page.locator('#preview-hint')).to_contain_text('1 / 3 페이지')
    page.locator('.pdf-tools [aria-label="다음 페이지"]').click()
    expect(page.locator('#preview-hint')).to_contain_text('2 / 3 페이지')
    page.locator('.pdf-tools [aria-label="확대"]').click()
    page.locator('.pdf-tools [aria-label="폭에 맞춤"]').click()
    expect(page.locator('#preview-body')).to_have_attribute('aria-busy','false')
    assert page.evaluate('document.querySelector("canvas.pdf-canvas").width*document.querySelector("canvas.pdf-canvas").height<=8*1024*1024')
    assert page.evaluate('pdfOptions.every(o=>o.withCredentials===false && o.isEvalSupported===false && o.disableAutoFetch===true && o.disableStream===true)')
    page.evaluate('window.lastCanvas=document.querySelector("canvas.pdf-canvas")')
    close()
    assert page.evaluate('pdfDestroyed===1 && lastCanvas.width===0 && lastCanvas.height===0')
    open_file('07-locked.pdf')
    expect(page.locator('.pdf-password')).to_be_visible()
    page.locator('.pdf-password input').fill('secret')
    page.locator('.pdf-password button').click()
    expect(page.locator('#preview-hint')).to_contain_text('1 / 3 페이지')
    assert not page.evaluate('JSON.stringify(calls.map(c=>c.url)).includes("secret")')
    close()
    open_file('01-mountains.jpg')
    page.evaluate('history.back()')
    expect(page.locator('#preview')).not_to_be_visible()
    assert not page.evaluate('document.body.classList.contains("preview-open")')
    assert not errors,errors
    browser.close()
print('PASS Chromium DOM fixtures: 320/360/390/768 mobile layout, 44px actions, selection bar, image fit/zoom, safe capped text, modal focus/Escape/back cleanup, native media fallback and cleanup, PDF API-double paging/zoom/password/cancellation. Not a real Media Chrome/PDF.js integration or browser-network test.')
