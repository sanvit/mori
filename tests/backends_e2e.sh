#!/bin/sh
# Runs mori against real WebDAV (Apache), FTP (vsftpd), and SFTP (OpenSSH)
# servers in Docker. Requires Docker and network access to pull images.
# Usage: tests/backends_e2e.sh [webdav] [ftp] [sftp]
set -eu
cd "$(dirname "$0")/.."
work=$(mktemp -d)
net=mori-e2e-$$
cleanup() {
	docker rm -f "$net-dav" "$net-ftp" "$net-sftp" >/dev/null 2>&1 || true
	docker network rm "$net" >/dev/null 2>&1 || true
	# Server containers may chown the shared data; remove it from inside Docker.
	docker run --rm -v "$work":/w python:3.13-alpine rm -rf /w/data >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT

docker run --rm --user "$(id -u):$(id -g)" -v "$PWD":/src:ro -v "$work":/out -w /src \
	-e CGO_ENABLED=0 -e GOCACHE=/tmp/gocache -e GOPATH=/tmp/go -e GOFLAGS=-buildvcs=false \
	golang:1.26-alpine go build -trimpath -o /out/mori ./cmd/mori
cp tests/backends_e2e.py "$work/smoke.py"
d="$work/data"
mkdir -p "$d/docs/sub/deep" "$d/docs/sub/empty"
printf '0123456789abcdefghijklmnopqrstuvwxyz0123' >"$d/README.md"
printf 'root' >"$d/docs/a.txt"
printf 'x' >"$d/docs/sub/x.txt"
printf '안녕' >"$d/docs/sub/deep/한글 +&%.txt"
: >"$d/docs/sub/zero.txt"
head -c 3000000 /dev/urandom >"$d/docs/big.bin"
chmod -R a+rwX "$d"

docker network create "$net" >/dev/null
docker run -d --name "$net-sftp" --network "$net" --network-alias mt-sftp -v "$d":/home/tester/files atmoz/sftp:alpine tester:secret:1000 >/dev/null
docker run -d --name "$net-ftp" --network "$net" --network-alias mt-ftp -e USERS="tester|secret|/home/tester|1000" -e ADDRESS=mt-ftp -v "$d":/home/tester/files delfer/alpine-ftp-server >/dev/null
docker run -d --name "$net-dav" --network "$net" --network-alias mt-dav -e AUTH_TYPE=Basic -e USERNAME=tester -e PASSWORD=secret -v "$d":/var/lib/dav/data bytemark/webdav >/dev/null
sleep 6
docker exec "$net-sftp" cat /etc/ssh/ssh_host_ed25519_key.pub | awk '{print "mt-sftp "$1" "$2}' >"$work/known_hosts"

[ $# -gt 0 ] || set -- webdav ftp sftp
docker run --rm --network "$net" -v "$work":/e2e python:3.13-alpine python3 /e2e/smoke.py "$@"
