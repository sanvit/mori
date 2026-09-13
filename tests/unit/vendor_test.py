"""Asset installer unit tests with synthetic tarballs; no real dependencies/network.

The production archive fetch is intentionally replaced here, not in the application.
Run python3 -m unittest discover -s tests -p 'vendor_test.py' -v.
"""
import base64
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('vendor', ROOT / 'tools/vendor.py')
vendor = importlib.util.module_from_spec(spec)
spec.loader.exec_module(vendor)


def archive(files):
    out = io.BytesIO()
    with tarfile.open(fileobj=out, mode='w:gz') as tar:
        for name, payload in files.items():
            info = tarfile.TarInfo('package/' + name)
            data = payload.encode()
            info.size = len(data)
            tar.addfile(info, io.BytesIO(data))
        link = tarfile.TarInfo('package/wasm/link.js')
        link.type = tarfile.SYMTYPE
        link.linkname = '/etc/passwd'
        tar.addfile(link)
    return out.getvalue()


class Installer(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        (self.root/'web').mkdir()
        self.pins = {'markdown-it':'15.0.1', 'media-chrome':'4.19.2', 'pdfjs-dist':'6.3.289'}
        (self.root/'package.json').write_text(json.dumps({'dependencies':self.pins}))
        (self.root/'web/preview.js').write_text('/_mori/vendor/markdown-it-15.0.1/ /_mori/vendor/media-chrome-4.19.2/ /_mori/vendor/pdfjs-6.3.289/')
        self.data = {
            'markdown-it':archive({'LICENSE':'test license', 'dist/browser/markdown-it.esm.min.mjs':'test markdown', 'dist/browser/unwanted.js':'skip'}),
            'media-chrome':archive({'LICENSE':'test license', 'dist/iife/index.js':'test media', 'dist/surprise.js':'skip'}),
            'pdfjs-dist':archive({'LICENSE':'test license', 'legacy/build/pdf.min.mjs':'test pdf', 'legacy/build/pdf.worker.min.mjs':'test worker', 'cmaps/test.bcmap':'test cmap', 'wasm/test.wasm':'test wasm', '../escape.js':'never extract', 'legacy/build/pdf.js':'skip'}),
        }
        self.contexts = [patch.object(vendor,'ROOT',self.root), patch.object(vendor,'OUT',self.root/'web/vendor'), patch.object(vendor,'fetch',self.fetch), patch.object(sys,'argv',['vendor.py'])]
        for ctx in self.contexts: ctx.start()
    def tearDown(self):
        for ctx in reversed(self.contexts): ctx.stop()
        self.tmp.cleanup()
    def fetch(self, url, maximum=None):
        for name, ver in self.pins.items():
            if url == f'https://registry.npmjs.org/{name}/{ver}':
                sri = 'sha512-' + base64.b64encode(hashlib.sha512(self.data[name]).digest()).decode()
                return json.dumps({'name':name, 'version':ver,'dist':{'tarball':f'https://registry.npmjs.org/{name}/-/{name}-{ver}.tgz', 'integrity':sri}}).encode()
            if url.endswith(f'/{name}-{ver}.tgz'): return self.data[name]
        raise AssertionError(url)
    def test_copy_verify_and_reject_tampering(self):
        vendor.main()
        self.assertTrue(vendor.verify(self.pins))
        files = list((self.root/'web/vendor').rglob('*'))
        self.assertFalse(any(p.name in ('escape.js','link.js','surprise.js','unwanted.js','pdf.js') for p in files))
        p = self.root/'web/vendor/media-chrome-4.19.2/index.js'
        p.write_text('tampered')
        self.assertFalse(vendor.verify(self.pins))
        with patch.object(sys,'argv',['vendor.py','--check']), self.assertRaises(SystemExit): vendor.main()
    def test_reject_bad_integrity(self):
        fetch = self.fetch
        def damaged(url, maximum=None):
            data = fetch(url,maximum)
            return data+b'broken' if url.endswith('.tgz') else data
        with patch.object(vendor,'fetch',damaged), self.assertRaisesRegex(RuntimeError,'integrity mismatch'): vendor.main()
        self.assertFalse((self.root/'web/vendor').exists())
    def test_reject_loader_version_mismatch(self):
        (self.root/'web/preview.js').write_text('/_mori/vendor/old-version/')
        with self.assertRaisesRegex(SystemExit,'Update the LIB paths'): vendor.main()
    def test_reject_registry_redirection(self):
        fetch = self.fetch
        def redirected(url, maximum=None):
            data=fetch(url, maximum)
            if not url.endswith('.tgz'):
                meta=json.loads(data)
                meta['dist']['tarball']='https://untrusted.test/package.tgz'
                return json.dumps(meta).encode()
            return data
        with patch.object(vendor,'fetch',redirected), self.assertRaisesRegex(RuntimeError,'Unexpected npm tarball'): vendor.main()

if __name__=='__main__': unittest.main()
