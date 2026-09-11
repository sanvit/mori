# Attribution and integration

mori is an independently implemented read-only object browser. h5ai inspired the directory-index interaction model. sanvit/s3-proxy supplies the optional object-cache service via a remote, pinned build context.

Upstream: https://github.com/sanvit/s3-proxy
Inspected source commit: 26e39a560d923c237dfec676eca70e15f5b5e1e6

The referenced upstream code is not bundled in this source archive or relicensed here. Its original ownership and applicable terms remain unchanged. Review upstream terms before redistributing a combined container image.

No external images, font binaries, tracking scripts, or CDN resources are bundled. System font family names in CSS refer to fonts already present on the viewer's device.

The SigV4 test fixture uses invented credentials solely for a deterministic test. It contains no actual AWS credentials.

## Optional browser preview dependencies (build-time acquisition)

The source integrates Media Chrome 4.19.2 (Mux, MIT) and PDF.js / pdfjs-dist 6.3.289 (Mozilla, Apache-2.0). Upstream distribution files are fetched during the Docker/assets build, not included in this source archive. The installer retains distributed license notices beside the collected assets. Refer to docs/PREVIEW.md and the official release pages for provenance. No h5ai or Plyr code is included.

## Go module dependencies

The server binary links github.com/jlaffaye/ftp (ISC), github.com/pkg/sftp (BSD-2-Clause) with github.com/kr/fs (BSD-3-Clause), and golang.org/x/crypto, golang.org/x/sys (BSD-3-Clause). Tests additionally use golang.org/x/net/webdav (BSD-3-Clause), github.com/fclairamb/ftpserverlib (MIT), and github.com/spf13/afero (Apache-2.0); these are not linked into the server binary. Module sources are fetched by the Go toolchain at build time and are not bundled in this source archive.
