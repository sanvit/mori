#!/usr/bin/env python3
"""Fetch pinned official npm tarballs, verify npm SHA-512 integrity, copy browser assets.

No npm lifecycle scripts, transitive packages, Node runtime or CDN at runtime.
All extraction is file-by-file to allowlisted locations; links are rejected.
Usage: python3 tools/vendor.py [--check] [--force]
"""
from __future__ import annotations
import argparse
import base64
import hashlib
import io
import json
from pathlib import Path, PurePosixPath
import shutil
import tarfile
import tempfile
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / 'web/vendor'
LIMIT = 80 * 1024 * 1024


def fetch(url: str, maximum: int = LIMIT) -> bytes:
    request = urllib.request.Request(url, headers={'User-Agent': 'mori-asset-builder/0.5'})
    with urllib.request.urlopen(request, timeout=60) as response:
        data = response.read(maximum + 1)
    if len(data) > maximum:
        raise RuntimeError('Asset response exceeds size limit')
    return data


def digest(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def verify(pins: dict[str, str]) -> bool:
    try:
        manifest = json.loads((OUT / 'manifest.json').read_text())
        if manifest['versions'] != pins:
            return False
        for name, expected in manifest['files'].items():
            p = PurePosixPath(name)
            if p.is_absolute() or '..' in p.parts or digest((OUT / name).read_bytes()) != expected:
                return False
        return len(manifest['files']) > 5
    except (OSError, ValueError, KeyError):
        return False


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true')
    parser.add_argument('--force', action='store_true')
    args = parser.parse_args()
    pins = json.loads((ROOT / 'package.json').read_text())['dependencies']
    if set(pins) != {'media-chrome', 'pdfjs-dist'}:
        raise SystemExit('Unexpected browser dependency list')
    # Prevent a dependency bump leaving the lazy-loader pointed at the old version.
    js = (ROOT / 'web/preview.js').read_text()
    for name, version in pins.items():
        dirname = ('pdfjs' if name == 'pdfjs-dist' else name) + '-' + version
        if '/_mori/vendor/' + dirname + '/' not in js:
            raise SystemExit(f'Update the LIB paths in web/preview.js for {name}@{version} first.')
    if verify(pins) and not args.force:
        print('Verified local preview assets:', ', '.join(f'{k}@{v}' for k,v in pins.items()))
        return
    if args.check:
        raise SystemExit('Preview assets are missing/changed. Run python3 tools/vendor.py with network access.')
    OUT.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='mori-vendor-', dir=OUT.parent) as tmp:
        stage = Path(tmp)
        manifest = {'versions': pins, 'packages': {}, 'files': {}}
        for name, version in pins.items():
            print('Fetching pinned assets:', name, version, flush=True)
            metadata = json.loads(fetch(f'https://registry.npmjs.org/{name}/{version}', 1024 * 1024))
            if metadata['name'] != name or metadata['version'] != version:
                raise RuntimeError('Registry returned a different package')
            expected_url = f'https://registry.npmjs.org/{name}/-/{name}-{version}.tgz'
            if metadata['dist']['tarball'] != expected_url:
                raise RuntimeError('Unexpected npm tarball URL')
            integrity = metadata['dist']['integrity']
            if not integrity.startswith('sha512-'):
                raise RuntimeError('Expected npm SHA-512 package integrity')
            data = fetch(expected_url)
            expected = base64.b64decode(integrity.removeprefix('sha512-'), validate=True)
            if hashlib.sha512(data).digest() != expected:
                raise RuntimeError('npm package integrity mismatch')
            target = ('media-chrome-' if name == 'media-chrome' else 'pdfjs-') + version
            manifest['packages'][name] = {'version': version, 'url': expected_url, 'integrity': integrity, 'sha256': digest(data)}
            count = 0
            with tarfile.open(fileobj=io.BytesIO(data), mode='r:gz') as archive:
                for member in archive.getmembers():
                    p = PurePosixPath(member.name)
                    if not member.isfile() or p.is_absolute() or '..' in p.parts or p.parts[0] != 'package':
                        continue
                    relative = '/'.join(p.parts[1:])
                    dest = None
                    if name == 'media-chrome':
                        if relative == 'dist/iife/index.js': dest = 'index.js'
                        elif relative in ('LICENSE', 'LICENSE.md'): dest = 'LICENSE'
                    else:
                        if relative == 'legacy/build/pdf.min.mjs': dest = 'pdf.mjs'
                        elif relative == 'legacy/build/pdf.worker.min.mjs': dest = 'pdf.worker.mjs'
                        elif relative == 'LICENSE': dest = 'LICENSE'
                        elif relative.startswith(('cmaps/', 'wasm/', 'standard_fonts/')) and (p.suffix in ('.bcmap', '.wasm', '.js', '.mjs', '.ttf', '.pfb') or p.name.upper().startswith('LICENSE')):
                            dest = relative
                    if dest is None:
                        continue
                    if member.size > 25 * 1024 * 1024:
                        raise RuntimeError('Individual asset too large')
                    source = archive.extractfile(member)
                    if source is None: raise RuntimeError('Missing archive file')
                    payload = source.read()
                    filename = target + '/' + dest
                    output = stage / filename
                    output.parent.mkdir(parents=True, exist_ok=True)
                    output.write_bytes(payload)
                    manifest['files'][filename] = digest(payload)
                    count += 1
            for filename in (['index.js', 'LICENSE'] if name == 'media-chrome' else ['pdf.mjs', 'pdf.worker.mjs', 'LICENSE']):
                if not (stage / target / filename).is_file():
                    raise RuntimeError(f'Published package lacks required asset: {filename}')
            print('Copied', count, 'files from', name, flush=True)
        (stage / 'manifest.json').write_text(json.dumps(manifest, indent=2, ensure_ascii=False) + '\n')
        if OUT.exists(): shutil.rmtree(OUT)
        shutil.copytree(stage, OUT)
    if not verify(pins): raise RuntimeError('Final asset verification failed')
    print('Preview assets ready in web/vendor (self-hosted).')

if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        raise SystemExit(f'Asset build failed: {error}')
