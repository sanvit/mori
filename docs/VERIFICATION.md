# 검증 기록 — 0.5.0 모바일 / 미리보기

이 기록은 이번 소스에 대한 검사입니다. 이전 0.4.0 결과는 `history-0.4/`에 분리했습니다. Go 1.23.2, Node 22.16.0, Python 3.13, Playwright/Chromium 환경입니다.

## 완료한 검사

| 검사 | 결과와 범위 |
|---|---|
| `go test -race -count=1 -cover ./...` | PASS, 86.6% statement coverage. 테스트 캐시 없이 실행. |
| `go vet ./...` | PASS. |
| Go 바이너리 빌드 | PASS. HTTP 통합 테스트가 실제 바이너리를 빌드하고 실행함. 단, 외부 preview assets는 없는 빌드. |
| `node --check web/app.js`, `web/preview.js` | PASS. 문법 검사. |
| `python3 tests/http_smoke.py` | PASS. 실제 mori + 모의 S3 HTTP, 네 가지 preview/download 모드 조합. |
| `python3 tests/ui_smoke.py` | PASS. 기존 단순 목록·선택·재귀 ZIP 준비·정렬·페이지·취소·오류 표시 회귀 검사. |
| `python3 tests/preview_dom.py` | PASS. 모바일 320/360/390/768px, 터치 버튼, 선택 바, 이미지 맞춤/확대, 텍스트 제한/안전 출력, dialog·포커스·Escape·뒤로 가기, 미디어 해제. **모의 응답·이미지 운송·PDF API 대역을 사용함.** |
| PDF API 대역 검사 | PASS. 페이지/확대 변경, 암호 입력 UI, render 취소/worker destroy 호출, Canvas 상한/해제. **실제 PDF.js 렌더링 검사는 아님.** |
| 미디어 DOM 검사 | PASS. native-controls fallback, metadata preload, playsinline, 자동 재생 없음, 닫을 때 pause/src 해제. **실제 Media Chrome custom elements/재생 검사는 아님.** |
| `python3 -m unittest discover -s tests -p vendor_test.py -v` | PASS, 4건. 합성 npm tarball로 allowlist 추출·hash 검사·변조/경로/잘못된 URL·버전 불일치 거부. 실제 npm 패키지는 사용하지 않음. |
| `python3 tests/compose_smoke.py` | PASS. PyYAML 구조 검사. Docker Compose 엔진의 실제 병합/기동 검사가 아님. |
| `python3 -m py_compile tools/vendor.py tests/*.py` | PASS. 새 실브라우저 테스트 스크립트 포함 Python 문법 검사. |

원문 출력은 이 폴더의 `*-output.txt`에 있습니다.

HTTP 검사에는 `/api/preview`가 URL을 만들 때 origin HEAD/list/body를 요청하지 않는지, 서명된 GET URL의 Range 요청과 서버 proxy Range 요청이 동일한 바이트를 돌려주는지 확인하는 항목을 추가했습니다. 서명된 GET은 모의 origin에서 botocore로 독립 검증합니다. 기본 단독 실행, 목록 캐시, HEAD/list 서버 측 처리, 1,005개 하위 파일의 여러 페이지 재귀 ZIP, Store·CRC·한도·단일 사용 계획도 기존 검사를 유지합니다.

새 Go 단위 검사는 미리보기 형식/안전한 MIME, descriptor 인증/키 검증/GetObject-only, 비밀 키 미노출, no-store, 정확한 presigned endpoint CSP, 정적 파일 MIME/ETag/경로 거부를 포함합니다.

## 저장소 백엔드 검사 (Unreleased)

Go 1.26.x(`golang:1.26-alpine`), Docker 환경에서 실행했습니다.

| 검사 | 결과와 범위 |
|---|---|
| `go test -race -count=1 -cover ./...` | PASS. WebDAV는 `golang.org/x/net/webdav`, FTP는 `ftpserverlib`(MLST 유무 둘 다), SFTP는 `pkg/sftp` 서버와 RSA·ed25519 호스트 키로 인프로세스 검사. 서버 패키지는 범용 백엔드 모형으로 Range·HEAD·304·412·416·ZIP·변경 감지·presigned 거부를 검사. |
| `python3 tests/http_smoke.py` | PASS. 기존 S3 경로 회귀 확인. 네 가지 proxy/presigned 조합, botocore 서명 검증, 재귀 ZIP. |
| `tests/backends_e2e.sh` | PASS. 실제 바이너리를 Apache WebDAV(`bytemark/webdav`), vsftpd(`delfer/alpine-ftp-server`), OpenSSH(`atmoz/sftp`)에 연결. 목록·없는 폴더 404·한글 파일명·빈 파일·Range·HEAD·304·3 MB 파일 해시·병렬 Range 12건·미리보기·재귀 ZIP·presigned 시작 거부·로그 비밀번호 미노출. |

실서버 검사에서 발견해 고친 문제: WebDAV 서버 루트 마운트의 경로 비교, vsftpd가 없는 폴더를 빈 목록으로 답하는 문제, vsftpd LIST와 MDTM 시각 정밀도 차이로 ZIP 버전 비교가 실패하던 문제, OpenSSH가 RSA 키를 먼저 제시해 ed25519만 있는 known_hosts 검증이 실패하던 문제. 각 문제에 회귀 테스트를 추가했습니다.

미검증: FTPS(`FTP_TLS=true`) 실서버 연결, SFTP 개인 키 인증 실서버 연결, Nextcloud 등 다른 WebDAV 구현, 대용량 폴더와 부하.

## 완료하지 않은 항목 — 배포 전 확인 필요

**실제 Media Chrome 4.19.2 / PDF.js 6.3.289 패키지를 내려받아 실행한 종단 검증은 완료하지 못했습니다.** 이 환경에서는 외부 npm/CDN DNS/다운로드와 브라우저 로컬 HTTP 탐색이 차단되었습니다. `python3 tools/vendor.py --check`는 실제 assets가 없어 exit 1을 반환합니다(`assets-check-output.txt`). 이것은 PASS가 아닙니다.

소스 ZIP은 실행 시 CDN을 사용하는 임시 우회가 아니라, 네트워크가 허용된 **Docker 빌드 단계에서 라이브러리를 수집하여 Go 바이너리에 임베드**하도록 구성했습니다. 빌드 단계는 자산 수집이 실패하면 종료합니다. 실제 두 tarball 취득 및 Docker 전체 빌드는 이 환경에서 검증하지 않았습니다.

`tests/preview_e2e.py`는 assets를 준비한 환경에서 실제 라이브러리·S3 모의 HTTP·CORS·서명·오디오 재생·PDF worker/페이지·이미지를 검사하는 스크립트입니다. ffmpeg가 있으면 WebM 재생도 검사합니다. 런타임 API 대역을 사용하지 않습니다. 이번 환경에서 이 스크립트의 **문법 검사만 통과**했으며 실제 종단 테스트 통과로 보고하지 않습니다. `tests/http_e2e.py`도 새 modal 동작에 맞춰 갱신했으나 브라우저 HTTP 실행은 미완료입니다.

실제 AWS/MinIO/R2 버킷과 CORS, 캐시 프록시 포함 Docker 기동, iPhone Safari/Android 실기기, fullscreen/PiP/플랫폼 음량 제한, 다양한 실제 PDF/코덱, 장시간 presigned URL 만료, 대형 PDF·영상 부하 시험도 미검증입니다.

## 재현

```sh
# 로컬/CI에서 네트워크를 허용한 뒤
python3 tools/vendor.py
python3 tools/vendor.py --check
go test -race -count=1 -cover ./...
go vet ./...
python3 tests/http_smoke.py
python3 tests/ui_smoke.py
python3 tests/preview_dom.py
python3 tests/http_e2e.py
python3 tests/preview_e2e.py
# 별도: 운영 .env를 설정하고 Docker 빌드/기동 및 실제 버킷 검증
docker compose up --build -d
```

`tests/requirements.txt`는 테스트 환경용입니다. 서버 런타임에는 Python/Node/테스트 패키지가 필요 없습니다. `tests/preview_dom.py`의 API 대역/표본은 `web/`나 실행 바이너리에 포함되지 않습니다.

## 화면 이미지

제공한 화면 이미지는 모의 목록/이미지 fixture를 브라우저로 렌더링한 결과입니다. 실제 사용자 S3 버킷, 실제 Media Chrome 재생 또는 PDF.js 렌더링의 증거가 아닙니다. 런타임 데모 모드를 추가하지 않았습니다.


## 2026-09-12 추가 검증

- `go test ./...`: 통과.
- `tests/preview_dom.py`: 모바일 빈 목록 열 너비, 새 탭 버튼, PDF 스크롤/확대/정리 통과.
- `tests/preview_e2e.py`: Chromium 실제 PDF.js worker/연속 스크롤 및 Media Chrome 오디오 통과.
- `MORI_TEST_BROWSER=webkit python3 tests/preview_e2e.py`: WebKit 실제 PDF.js worker/연속 스크롤 통과.
- 두 브라우저 모두 proxy/presigned 및 HTML 스크립트/외부 리소스 네 조합을 검사.
  앱 iframe/새 탭의 HTML 스타일, 인라인/외부 스크립트 허용·차단 확인.
- 아이폰 실기기, 운영 저장소, WebKit 미디어, WebM 재생은 검증하지 않음.

위 추가 검증은 이전 기록의 미검증 항목 중 명시한 범위만 갱신합니다.


## iOS 18 PDF 후속 수정 검증

WebKit 18.4 (Playwright 1.51.0)에서 기존 구현의 PDF 표시 후 실패를 재현했습니다.
`getTextContent()`의 스트림 비동기 이터레이터 의존성을 `streamTextContent().getReader()`로
대체한 뒤 동일한 실제 PDF.js 테스트의 proxy/presigned 및 HTML 설정 조합이 통과했습니다.
아이폰 iOS 18.7.2 실기기 실행을 의미하지는 않습니다.

DOM 회귀 검사는 스트림 비동기 이터레이터가 없는 환경, 텍스트 추출 실패 후 페이지 유지,
추출 텍스트 크기 제한 및 스트림 취소를 검사합니다.
`go test ./internal/server ./internal/media ./internal/s3`는 PDF GET/HEAD/Range 및
한글 파일명, proxy/presigned의 inline/attachment 응답 검사를 포함해 통과했습니다.
