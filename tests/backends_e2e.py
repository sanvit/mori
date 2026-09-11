"""Live-server smoke test for the WebDAV, FTP, and SFTP backends.

Run via tests/backends_e2e.sh, which starts Apache WebDAV, vsftpd, and OpenSSH
containers and executes this script next to a freshly built mori binary.
"""
import hashlib, io, json, os, subprocess, sys, time, urllib.parse, urllib.request, urllib.error, zipfile
from concurrent.futures import ThreadPoolExecutor

BASE = {
    "webdav": {"STORAGE_BACKEND": "webdav", "WEBDAV_URL": "http://mt-dav/", "WEBDAV_USERNAME": "tester", "WEBDAV_PASSWORD": "secret"},
    "ftp": {"STORAGE_BACKEND": "ftp", "FTP_ADDR": "mt-ftp", "FTP_USERNAME": "tester", "FTP_PASSWORD": "secret", "FTP_PATH": "files"},
    "sftp": {"STORAGE_BACKEND": "sftp", "SFTP_ADDR": "mt-sftp", "SFTP_USERNAME": "tester", "SFTP_PASSWORD": "secret", "SFTP_KNOWN_HOSTS": "/e2e/known_hosts", "SFTP_PATH": "/files"},
}
BIG = open("/e2e/data/docs/big.bin", "rb").read()
PORT = 18090

def req(path, method="GET", headers=None, data=None):
    r = urllib.request.Request(f"http://127.0.0.1:{PORT}{path}", method=method, headers=headers or {}, data=data)
    try:
        with urllib.request.urlopen(r, timeout=30) as resp:
            return resp.status, dict(resp.headers), resp.read()
    except urllib.error.HTTPError as e:
        return e.code, dict(e.headers), e.read()

STEP = [""]
def check(cond, msg):
    STEP[0] = repr(msg)[:80]
    if not cond:
        raise AssertionError(msg)

def run(name, env):
    full = dict(os.environ, BROWSER_PUBLIC="true", BROWSER_LISTEN_ADDR=f"127.0.0.1:{PORT}", **env)
    # Presigned delivery must be refused at startup for non-S3 backends.
    bad = subprocess.run(["/e2e/mori"], env=dict(full, BROWSER_DOWNLOAD_MODE="presigned"), capture_output=True, timeout=10, cwd="/tmp")
    check(bad.returncode != 0 and b"require STORAGE_BACKEND=s3" in bad.stderr, f"presigned accepted: {bad.stderr}")
    p = subprocess.Popen(["/e2e/mori"], env=full, stderr=subprocess.PIPE, cwd="/tmp")
    try:
        for _ in range(50):
            try:
                if req("/healthz")[0] == 200: break
            except Exception: time.sleep(0.1)
        s, h, b = req("/api/config"); cfg = json.loads(b)
        check(cfg["backend"] == name and cfg["downloadMode"] == "proxy" and cfg["previewMode"] == "proxy", cfg)

        s, h, b = req("/api/list"); l = json.loads(b)
        keys = [e["key"] for e in l["entries"]]
        check(s == 200 and keys == ["docs/", "README.md"], (s, keys))
        s, h, b = req("/api/list?prefix=docs/"); l = json.loads(b)
        check([e["key"] for e in l["entries"]] == ["docs/sub/", "docs/a.txt", "docs/big.bin"], l)
        big = [e for e in l["entries"] if e["key"] == "docs/big.bin"][0]
        check(big["size"] == len(BIG) and big["etag"] and big["modified"], big)
        s, h, b = req("/api/list?prefix=docs/sub/"); l = json.loads(b)
        check([e["key"] for e in l["entries"]] == ["docs/sub/deep/", "docs/sub/empty/", "docs/sub/x.txt", "docs/sub/zero.txt"], l)
        s, h, b = req("/api/list?prefix=nope/"); check(s == 404, ("missing folder", s, b))

        s, h, b = req("/api/object?key=README.md")
        check(s == 200 and b == b"0123456789abcdefghijklmnopqrstuvwxyz0123" and h["Content-Type"].startswith("text/plain") and "sandbox" in h["Content-Security-Policy"], (s, h))
        etag = h["Etag"]
        s, h, b = req("/api/object?key=README.md", headers={"Range": "bytes=10-15"})
        check(s == 206 and b == b"abcdef" and h["Content-Range"] == "bytes 10-15/40", (s, b, h))
        s, h, b = req("/api/object?key=README.md", "HEAD"); check(s == 200 and h["Content-Length"] == "40" and b == b"", (s, h))
        s, h, b = req("/api/object?key=README.md", headers={"If-None-Match": etag}); check(s == 304, s)
        s, h, b = req("/api/object?" + urllib.parse.urlencode({"key": "docs/sub/deep/한글 +&%.txt"}) + "&download=1")
        check(s == 200 and b == "안녕".encode() and h["Content-Disposition"].startswith("attachment"), (s, b, h))
        s, h, b = req("/api/object?key=docs/sub/zero.txt"); check(s == 200 and b == b"", (s, b))
        s, h, b = req("/api/object?key=missing.txt"); check(s == 404, s)
        s, h, b = req("/api/object?key=docs/big.bin")
        check(s == 200 and hashlib.sha256(b).digest() == hashlib.sha256(BIG).digest(), ("big full", s, len(b)))

        def ranged(i):
            start = i * 250_000 + 7
            return i, req("/api/object?key=docs/big.bin", headers={"Range": f"bytes={start}-{start + 99_999}"})
        with ThreadPoolExecutor(8) as ex:
            for i, (s, h, b) in ex.map(ranged, range(12)):
                start = i * 250_000 + 7
                check(s == 206 and b == BIG[start:start + 100_000], ("parallel range", i, s, len(b)))
        s, h, b = req("/api/object?key=docs/big.bin", headers={"Range": f"bytes={len(BIG)-5}-"})
        check(s == 206 and b == BIG[-5:], ("tail range", s))

        s, h, b = req("/api/preview?key=README.md"); pv = json.loads(b)
        check(pv["kind"] == "text" and pv["mode"] == "proxy" and pv["url"] == "/api/object?key=README.md", pv)

        body = json.dumps({"prefix": "docs/", "keys": ["docs/sub/", "docs/a.txt", "docs/big.bin"]}).encode()
        s, h, b = req("/api/archive", "POST", {"Content-Type": "application/json", "X-Mori-Request": "1"}, body)
        check(s == 200, ("zip prepare", s, b)); plan = json.loads(b)
        check(plan["files"] == 5 and plan["size"] == len(BIG) + 4 + 1 + 6, plan)
        s, h, b = req(plan["url"])
        check(s == 200 and h["Content-Type"] == "application/zip", (s, h))
        z = zipfile.ZipFile(io.BytesIO(b)); names = sorted(z.namelist())
        check(names == sorted(["a.txt", "big.bin", "sub/", "sub/deep/", "sub/deep/한글 +&%.txt", "sub/empty/", "sub/x.txt", "sub/zero.txt"]), names)
        check(z.read("big.bin") == BIG and z.read("sub/deep/한글 +&%.txt") == "안녕".encode() and z.testzip() is None, "zip payload")
        print(f"{name}: PASS")
    finally:
        p.terminate(); _, err = p.communicate(timeout=10)
        if os.environ.get("SHOWLOG"): print("last ok step:", STEP[0]); print(err.decode()[-1500:])
        leaked = [line for line in err.decode().splitlines() if "secret" in line]
        check(not leaked, leaked)

def start(env):
    p = subprocess.Popen(["/e2e/mori"], env=dict(os.environ, BROWSER_PUBLIC="true", BROWSER_LISTEN_ADDR=f"127.0.0.1:{PORT}", **env), stderr=subprocess.PIPE, cwd="/tmp")
    for _ in range(50):
        try:
            if req("/healthz")[0] == 200: return p
        except Exception: time.sleep(0.1)
    return p

def run_base_path(name, env):
    """STORAGE_BASE_PATH publishes docs/sub/ as the root; nothing above it is reachable."""
    p = start(dict(env, STORAGE_BASE_PATH="/docs/sub/"))
    try:
        s, h, b = req("/api/list"); l = json.loads(b)
        check(s == 200 and [e["key"] for e in l["entries"]] == ["deep/", "empty/", "x.txt", "zero.txt"], ("base root", s, b))
        check(b"docs" not in b and b"docs" not in req("/api/config")[2], "base path leaked")
        s, h, b = req("/api/list?prefix=deep/"); check(s == 200 and json.loads(b)["entries"][0]["key"] == "deep/한글 +&%.txt", ("base child", s, b))
        s, h, b = req("/api/object?key=x.txt"); check(s == 200 and b == b"x", ("base object", s, b))
        s, h, b = req("/api/object?key=x.txt", headers={"Range": "bytes=0-0"}); check(s == 206 and b == b"x", ("base range", s))
        for key in ["README.md", "a.txt", "big.bin"]:
            s, h, b = req("/api/object?" + urllib.parse.urlencode({"key": key})); check(s == 404, ("outside base reachable", key, s))
        for key in ["../a.txt", "../../README.md"]:
            s, h, b = req("/api/object?" + urllib.parse.urlencode({"key": key})); check(s == 400, ("escape", key, s))
        s, h, b = req("/api/list?prefix=../"); check(s == 400, ("escape list", s))
        body = json.dumps({"prefix": "", "keys": ["deep/", "x.txt"]}).encode()
        s, h, b = req("/api/archive", "POST", {"Content-Type": "application/json", "X-Mori-Request": "1"}, body)
        check(s == 200, ("base zip prepare", s, b)); plan = json.loads(b)
        s, h, b = req(plan["url"])
        z = zipfile.ZipFile(io.BytesIO(b))
        check(sorted(z.namelist()) == ["deep/", "deep/한글 +&%.txt", "x.txt"] and z.read("x.txt") == b"x", ("base zip", z.namelist()))
    finally:
        p.terminate(); p.communicate(timeout=10)
    p = start(dict(env, STORAGE_BASE_PATH="docs/nope"))
    try:
        s, h, b = req("/api/list"); check(s == 404, ("missing base path must be 404", s, b))
    finally:
        p.terminate(); p.communicate(timeout=10)
    print(f"{name} base path: PASS")

failed = False
for name in sys.argv[1:]:
    for label, fn in [(name, run), (name + " base path", run_base_path)]:
        try:
            fn(name, BASE[name])
        except Exception as e:
            failed = True
            print(f"{label}: FAIL {type(e).__name__}: {e}"[:600])
sys.exit(1 if failed else 0)
