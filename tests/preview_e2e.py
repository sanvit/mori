"""Actual self-hosted Media Chrome/PDF.js + Chromium + HTTP S3 fixture.

Requires `make assets`, Go, test requirements and Chromium. ffmpeg is optional
for a real WebM playback check. Unlike preview_dom.py, this suite does NOT replace
fetch, media elements or library imports. Failure/skip must not count as a pass.
Run python3 tests/preview_e2e.py on a machine permitting local browser navigation.
"""
from pathlib import Path
import base64
import io
import math
import os
import shutil
import struct
import subprocess
import tempfile
import threading
import time
import urllib.request
import wave
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler
from itertools import product
from playwright.sync_api import sync_playwright, expect
import http_e2e as fixture


def simple_pdf():
    # Test-only, two-page ASCII PDF generated without PDF or font dependencies.
    objects = [b'<< /Type /Catalog /Pages 2 0 R >>', b'<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 2 >>']
    for content_id in (4,6):
        objects.append(f'<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 7 0 R >> >> /Contents {content_id} 0 R >>'.encode())
        text=f'BT /F1 24 Tf 50 740 Td (mori preview fixture - page {content_id//2-1}) Tj ET'.encode()
        objects.append(b'<< /Length '+str(len(text)).encode()+b' >>\nstream\n'+text+b'\nendstream')
    objects.append(b'<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>')
    out=bytearray(b'%PDF-1.4\n'); positions=[]
    for index,obj in enumerate(objects,1):
        positions.append(len(out)); out.extend(f'{index} 0 obj\n'.encode()+obj+b'\nendobj\n')
    offset=len(out); out.extend(f'xref\n0 {len(objects)+1}\n0000000000 65535 f \n'.encode())
    for pos in positions: out.extend(f'{pos:010d} 00000 n \n'.encode())
    out.extend(f'trailer\n<< /Size {len(objects)+1} /Root 1 0 R >>\nstartxref\n{offset}\n%%EOF\n'.encode())
    return bytes(out)


def wav_data():
    data=io.BytesIO()
    with wave.open(data,'wb') as out:
        out.setnchannels(1); out.setsampwidth(2); out.setframerate(16000)
        out.writeframes(b''.join(struct.pack('<h',int(2500*math.sin(i*2*math.pi*220/16000))) for i in range(32000)))
    return data.getvalue()


class HTMLResources(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header('Access-Control-Allow-Origin', '*')
        if self.path == '/style.css':
            self.send_header('Content-Type','text/css'); self.end_headers()
            self.wfile.write(b'h1 { background-color: rgb(0, 0, 255); }')
        else:
            self.send_header('Content-Type','text/javascript'); self.end_headers()
            self.wfile.write(b'document.body.dataset.external="yes";')
    def log_message(self, *args): pass


def main():
    subprocess.run(['python3','tools/vendor.py','--check'],cwd=fixture.ROOT,check=True)
    resources=ThreadingHTTPServer(('127.0.0.1',0),HTMLResources)
    threading.Thread(target=resources.serve_forever,daemon=True).start()
    fixture.DATA.update({
        'public/00-audio.wav':wav_data(),
        'public/02-preview.pdf':simple_pdf(),
        'public/04-page.html':b'<h1 style="color: rgb(255, 0, 0)">Rendered HTML</h1><script>document.body.dataset.executed="yes"</script>',
        'public/03-image.png':base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGOomLYKAANCAbkKDdeAAAAAAElFTkSuQmCC'),
    })
    fixture.DATA['public/04-page.html'] += f'<link rel="stylesheet" href="http://127.0.0.1:{resources.server_port}/style.css"><script src="http://127.0.0.1:{resources.server_port}/script.js"></script>'.encode()
    with tempfile.TemporaryDirectory(prefix='mori-preview-e2e-') as tmp:
        binary=Path(tmp)/'mori'
        subprocess.run(['go','build','-o',str(binary),'./cmd/mori'],cwd=fixture.ROOT,check=True)
        has_video=bool(shutil.which('ffmpeg'))
        if has_video:
            clip=Path(tmp)/'clip.webm'
            subprocess.run(['ffmpeg','-v','error','-f','lavfi','-i','testsrc2=size=160x90:rate=10','-t','2','-an','-c:v','libvpx-vp9','-y',str(clip)],check=True)
            fixture.DATA['public/01-video.webm']=clip.read_bytes()
        origin=ThreadingHTTPServer(('127.0.0.1',0),fixture.S3Fixture)
        threading.Thread(target=origin.serve_forever,daemon=True).start()
        try:
            with sync_playwright() as pw:
                options={'headless':True,'args':['--no-sandbox']}
                executable=os.getenv('CHROMIUM_PATH') or shutil.which('chromium')
                if executable: options['executable_path']=executable
                engine=os.getenv('MORI_TEST_BROWSER', 'chromium')
                browser=getattr(pw,engine).launch(**(options if engine=='chromium' else {'headless':True}))
                for mode,scripts,external in product(('proxy','presigned'), (False,True), (False,True)):
                    port=fixture.free_port(); base=f'http://127.0.0.1:{port}'
                    env={k:v for k,v in os.environ.items() if not k.startswith(('BROWSER_','S3_'))}
                    env.update(BROWSER_LISTEN_ADDR=f'127.0.0.1:{port}',BROWSER_USERNAME='tester',BROWSER_PASSWORD='test-browser-password',BROWSER_PUBLIC='false',BROWSER_PREVIEW_MODE=mode,BROWSER_HTML_PREVIEW_ENABLED='true',BROWSER_HTML_PREVIEW_SCRIPTS=str(scripts).lower(),BROWSER_HTML_PREVIEW_EXTERNAL_RESOURCES=str(external).lower(),BROWSER_DOWNLOAD_MODE='proxy',BROWSER_PROXY_URL='',S3_ENDPOINT=f'http://127.0.0.1:{origin.server_port}',S3_REGION='ap-northeast-2',S3_BUCKET='test-bucket',S3_PREFIX='public/',S3_FORCE_PATH_STYLE='true',S3_ACCESS_KEY_ID='TESTACCESS',S3_SECRET_ACCESS_KEY='test-secret-key')
                    process=subprocess.Popen([str(binary)],cwd=tmp,env=env,stdout=subprocess.DEVNULL,stderr=subprocess.PIPE)
                    try:
                        for _ in range(100):
                            try:
                                urllib.request.urlopen(base+'/healthz',timeout=.2).close();break
                            except OSError: time.sleep(.05)
                        else: raise RuntimeError('mori did not start')
                        context=browser.new_context(viewport={'width':390,'height':844},is_mobile=True,has_touch=True,device_scale_factor=3,http_credentials={'username':'tester','password':'test-browser-password','origin':base})
                        page=context.new_page(); errors=[]; page.on('pageerror',lambda e:errors.append(str(e)))
                        page.goto(base)
                        expect(page.locator('.file-row')).not_to_have_count(0)
                        for _ in range(20):
                            more=page.locator('#load-more')
                            if not more.is_visible(): break
                            more.click(); expect(page.locator('#refresh')).to_be_enabled()
                        def open_file(name):
                            page.locator(f'tr[data-key="{name}"] .entry-link').click()
                            expect(page.locator('#preview')).to_be_visible()
                        def close():
                            page.locator('#preview-close').click(); expect(page.locator('#preview')).not_to_be_visible();page.wait_for_timeout(100)
                        open_file('03-image.png'); expect(page.locator('#preview-body')).to_have_attribute('aria-busy','false'); close()
                        for name,tag in ([('00-audio.wav','audio')]+([('01-video.webm','video')] if has_video else []) if engine=='chromium' else []):
                            open_file(name); expect(page.locator('.mori-player.enhanced')).to_be_visible(timeout=20000)
                            assert page.evaluate('!!customElements.get("media-controller")')
                            page.locator('.player-chrome media-play-button').click()
                            page.wait_for_function(f'() => document.querySelector("{tag}").currentTime>0',timeout=15000)
                            page.evaluate(f'window.lastMedia=document.querySelector("{tag}")')
                            close(); assert page.evaluate('lastMedia.paused && !lastMedia.getAttribute("src")')
                        open_file('02-preview.pdf')
                        expect(page.locator('.pdf-page')).to_have_count(2,timeout=30000)
                        expect(page.locator('.pdf-page').first.locator('[role=document]')).to_contain_text('mori')
                        page.locator('.pdf-stage').evaluate('(s)=>s.scrollTop=s.scrollHeight')
                        expect(page.locator('.pdf-page').nth(1).locator('[role=document]')).to_contain_text('mori')
                        close()
                        open_file('04-page.html')
                        expect(page.frame_locator('.html-preview').locator('h1')).to_have_text('Rendered HTML')
                        expect(page.frame_locator('.html-preview').locator('h1')).to_have_css('color','rgb(255, 0, 0)')
                        expect(page.frame_locator('.html-preview').locator('body')).to_have_attribute('data-executed','yes') if scripts else expect(page.frame_locator('.html-preview').locator('body')).not_to_have_attribute('data-executed','yes')
                        expect(page.frame_locator('.html-preview').locator('h1')).to_have_css('background-color','rgb(0, 0, 255)' if external else 'rgba(0, 0, 0, 0)')
                        if scripts and external: expect(page.frame_locator('.html-preview').locator('body')).to_have_attribute('data-external','yes')
                        else: expect(page.frame_locator('.html-preview').locator('body')).not_to_have_attribute('data-external','yes')
                        with context.expect_page() as opened:
                            page.locator('#preview-original').click()
                        original=opened.value
                        expect(original.locator('h1')).to_have_text('Rendered HTML')
                        expect(original.locator('h1')).to_have_css('color','rgb(255, 0, 0)')
                        if scripts: expect(original.locator('body')).to_have_attribute('data-executed','yes')
                        else: expect(original.locator('body')).not_to_have_attribute('data-executed','yes')
                        expect(original.locator('h1')).to_have_css('background-color','rgb(0, 0, 255)' if external else 'rgba(0, 0, 0, 0)')
                        if scripts and external: expect(original.locator('body')).to_have_attribute('data-external','yes')
                        else: expect(original.locator('body')).not_to_have_attribute('data-external','yes')
                        original.close(); close()
                        open_file('README.md'); expect(page.locator('.preview-code')).to_contain_text('# mori');close()
                        assert not page.evaluate('document.documentElement.scrollWidth>innerWidth')
                        assert not errors,errors
                        context.close()
                        print(f'PASS {engine}: {mode=}, PDF worker/scroll, image/text, HTML iframe/new tab {scripts=} {external=}; audio tested={engine=="chromium"}, WebM tested={has_video and engine=="chromium"}.')
                    finally:
                        process.terminate()
                        try: process.communicate(timeout=5)
                        except subprocess.TimeoutExpired: process.kill();process.communicate()
                browser.close()
        finally:
            origin.shutdown();origin.server_close()
            resources.shutdown();resources.server_close()
    assert not fixture.ERRORS,fixture.ERRORS

if __name__=='__main__': main()
