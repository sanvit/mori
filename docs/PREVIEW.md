# 모바일 / 미리보기 — 0.5.0

## 화면과 조작

파일 목록은 h5ai처럼 경로와 표만 유지합니다. 지원 파일의 이름을 누르면 미리보기가 열리고, 폴더를 누르면 해당 폴더로 이동합니다. 다운로드 아이콘과 ZIP 선택은 별개입니다. Ctrl/Cmd 클릭이나 가운데 버튼은 기존 원본 링크 동작을 유지합니다.

작은 화면에서는 수정일·크기를 파일명 아래로 옮기고 정렬 선택 메뉴를 제공합니다. 44px 다운로드/미리보기 버튼, 넓은 체크박스 터치 영역, 가로 스크롤 경로, safe-area 여백을 사용합니다. 선택 항목이 있을 때만 하단에 ZIP/선택 해제 바가 생깁니다. 모바일 미리보기는 전체 화면, 데스크톱은 대화상자입니다. 닫기 버튼, Esc, 브라우저 뒤로 가기로 닫습니다. 닫거나 파일을 바꾸면 요청 취소·미디어 정지·PDF 작업 해제를 수행합니다. 포커스는 원래 파일 링크로 돌아갑니다.

이전/다음 파일은 **현재 화면에 불러온 미리보기 지원 파일** 사이에서만 이동합니다. 아직 불러오지 않은 페이지나 하위 폴더까지 미리 읽지 않습니다. 이미지에서는 확대/축소와 화면 맞춤, 오디오/영상에서는 재생·탐색·음량·속도, 영상에서는 지원 브라우저의 전체 화면/PiP를 제공합니다. 좁은 화면에서 일부 보조 컨트롤은 숨기며, 모바일 음량 조작은 OS 제한을 따릅니다. 자동 재생은 켜지 않습니다.

## 형식

| 종류 | 미리보기 대상 | 처리 |
|---|---|---|
| 이미지 | JPEG, PNG, GIF, WebP, AVIF, BMP, ICO | 브라우저 이미지 디코더, 맞춤/확대/축소 |
| 영상 | MP4, M4V, WebM, OGV, MOV | Media Chrome 컨트롤 + native video |
| 소리 | MP3, M4A, AAC, WAV, OGG/OGA, Opus, FLAC | Media Chrome 컨트롤 + native audio |
| PDF | PDF | PDF.js, 연속 스크롤, 확대/폭 맞춤, 암호 입력 |
| 텍스트 | TXT, Markdown, JSON, CSV, YAML, 코드, 로그, HTML, SVG 등 | UTF-8 원문, 줄바꿈 전환, 처음 1 MiB |
| 그 외 | ZIP, Office, HEIC, TIFF, MKV 등 | 원본 다운로드 |

확장자는 뷰어 선택용이지 재생 보증이 아닙니다. 같은 MP4/MOV라도 코덱·프로파일·OS·브라우저에 따라 재생되지 않을 수 있습니다. 서버 변환/transcoding, HEIC/TIFF 변환, Office 뷰어, HLS/DASH manifest 및 하위 세그먼트 서명은 구현하지 않았습니다. 실패하면 안내와 재시도/다운로드를 표시합니다.

기본 설정에서 HTML·SVG·Markdown은 마크업으로 실행하지 않고 `textContent`로만 표시합니다. `.ts`는 TypeScript 소스로 처리합니다. PDF는 연속 스크롤 미리보기이며 편집기·전자서명 검증기·양식 작성기는 아닙니다. PDF 스크립트/XFA를 실행하지 않습니다. 암호는 PDF.js에 로컬로 전달하고 서버/로그/스토리지에 저장하지 않습니다. 페이지에는 스크린리더용 텍스트를 제공하지만 일반 PDF 뷰어의 선택 가능한 텍스트 레이어나 검색/주석 UI는 넣지 않았습니다.

## 유지보수 중인 의존성

2026-09-09 공식 GitHub latest stable 확인 기준입니다. 향후 유지보수나 호환성을 보증한다는 의미는 아닙니다. Plyr의 유지보수 중단을 단정하지 않고, 이번 구현에는 아래 안정판을 선택했습니다.

| 역할 | 고정 버전 | 공식 릴리스 |
|---|---|---|
| 오디오/영상 컨트롤 | Mux Media Chrome 4.19.2 | 2026-06-10 |
| PDF 엔진 | Mozilla PDF.js 6.3.289 | 2026-08-29 |

Media Chrome: https://github.com/muxinc/media-chrome/releases/tag/v4.19.2

PDF.js: https://github.com/mozilla/pdf.js/releases/tag/v6.3.289

Media Chrome의 번들된 Web Components와 PDF.js legacy 빌드를 사용합니다. 별도의 React/Vue, UI 프레임워크, 플레이리스트 서비스는 없습니다. 이미지·텍스트에는 추가 라이브러리를 사용하지 않습니다.

## 빌드: 외부 요청은 빌드할 때만

이 소스 ZIP에는 두 라이브러리의 배포 파일을 미리 넣지 않았습니다. **Docker 빌드의 assets 단계가 고정 버전의 공식 npm 패키지를 내려받습니다.** 이 단계는 인터넷 접근이 필요하며, 실패하면 이미지 빌드도 실패합니다. 고정 버전의 registry metadata와 tarball URL, SHA-512 integrity를 확인하고 필요한 브라우저 파일만 복사합니다. npm lifecycle 스크립트나 임의 transitive 패키지를 실행하지 않습니다.

```sh
# Docker: assets 수집 -> Go 테스트/빌드 -> 단일 런타임 컨테이너
docker compose up --build -d

# 로컬: Python 3 + Go, 자산을 먼저 준비합니다.
make assets
make run
# 또는 make build -> bin/mori
```

생성 파일은 `web/vendor/`에 있고 `manifest.json`에 패키지 무결성과 파일 SHA-256을 기록합니다. PDF.js worker, CMap, WASM 및 표준 폰트 데이터도 이 빌드 단계에서 가져옵니다. 실행 시에는 Go 바이너리에 임베드한 파일을 **같은 서버의 버전별 `/vendor/` 경로**에서 제공하며 외부 CDN, Node/Python 서버가 필요 없습니다. 파일 미리보기를 열 때 필요한 엔진만 지연 로드합니다. 외부 S3 직접 요청은 `presigned` 모드의 파일 본문 접근에만 사용합니다.

인터넷이 차단된 환경은 네트워크가 가능한 빌드 환경에서 이미지를 만든 뒤 옮기세요. `go run ./cmd/mori`만 하고 assets를 준비하지 않으면 서버는 경고를 내며 목록/다운로드는 동작하나, Media Chrome 대신 기본 재생 컨트롤이 나오고 PDF.js 미리보기는 사용할 수 없습니다. 의도치 않은 불완전 빌드를 피하려면 `make build` 또는 Dockerfile을 사용하세요.

## 기존 전달 방식 유지

```dotenv
BROWSER_PROXY_URL=
BROWSER_DOWNLOAD_MODE=presigned
BROWSER_PREVIEW_MODE=proxy
BROWSER_PRESIGN_TTL=15m
```

- `proxy`: `/api/object`를 통해 mori가 중계합니다. `BROWSER_PROXY_URL`이 비어 있으면 직접 S3, 값이 있으면 선택 캐시를 이용합니다.
- `presigned`: `/api/preview`가 인증 후 **GetObject GET URL**을 JSON으로 발급하고 뷰어가 S3에서 직접 읽습니다. PDF Range 요청도 같은 GET URL을 사용합니다. 브라우저 인증 정보를 S3에 넘기지 않습니다.

새 `/api/preview`는 URL을 준비할 뿐 S3 HEAD·목록·파일 본문을 읽지 않습니다. 응답은 `private, no-store`입니다. URL은 목록에 미리 서명하지 않으며 history/주소 표시줄/로컬 스토리지에 저장하지 않습니다. 사용자에게 접근 권한이 있는 서명 URL인 만큼 개발자 도구나 네트워크 로그에서는 확인할 수 있고, 유효기간 동안 공유받은 사람도 사용할 수 있습니다.

개별 다운로드의 `/api/object?download=1`과 원본 열기는 기존 GET/307 동작을 유지합니다. HEAD·목록·ZIP을 presign하지 않습니다. ZIP은 전달 모드에 관계없이 서버를 거쳐 무압축 스트리밍합니다. 재시도는 미리보기 URL을 새로 발급하지만 만료/오류를 이유로 모드를 자동 변경하거나 cache proxy로 우회하지 않습니다. 긴 영상은 나중의 Range 요청 시 URL이 만료될 수 있으므로 적절한 TTL을 설정하세요.

## Presigned PDF/텍스트: S3 CORS

PDF.js와 텍스트 뷰어가 다른 출처의 데이터를 읽으려면 S3 버킷에 CORS가 필요합니다. `docs/s3-cors.example.json`의 주소를 **실제 mori 웹 화면의 출처**로 바꾸고 해당 버킷에 적용하세요. 서버 내부 주소를 넣는 것이 아닙니다. 예를 들어 화면이 `https://files.example.com`이면 그 출처를 허용합니다.

```json
[
  {
    "AllowedOrigins": ["https://files.example.com"],
    "AllowedMethods": ["GET"],
    "AllowedHeaders": ["Range", "If-Range"],
    "ExposeHeaders": ["Accept-Ranges", "Content-Range", "Content-Length", "ETag"],
    "MaxAgeSeconds": 3600
  }
]
```

CORS는 버킷을 공개하거나 읽기 권한을 부여하는 설정이 아닙니다. AWS S3는 설정에 맞춰 OPTIONS preflight에 응답하므로 AllowedMethods에 OPTIONS를 추가하지 않습니다. 여기서 클라이언트에게 발급하는 서명은 GET뿐입니다. 이미지·native 미디어는 보통 JS 읽기 없이 표시할 수 있지만 코덱/서버 정책에 따른 차이는 있습니다. 이 설정 예제는 PDF·텍스트의 직접 읽기에 필요한 범위를 포함합니다.

S3 호환 서비스의 관리 화면에서 동일한 규칙을 적용할 수 있습니다. 이미 다른 CORS 규칙이 있다면 전체 설정을 덮어쓰지 말고 병합하세요. 공개 endpoint는 사용자 브라우저에서 접속 가능해야 하며, HTTPS 화면에서는 HTTPS object endpoint를 사용하세요. 내부/외부 주소가 다르면 `BROWSER_PRESIGN_ENDPOINT`를 설정합니다. CSP도 그 실제 호스트만 허용합니다.

AWS CORS 설명: https://docs.aws.amazon.com/AmazonS3/latest/userguide/cors.html

## 자원 사용 / 제한

미리보기용 서버 변환이나 원본 전체 Blob 복사는 추가하지 않았습니다. 그렇다고 모든 형식이 부분 다운로드되는 것은 아닙니다. 이미지는 일반 이미지처럼 원본을 받고 디코딩하므로 큰 이미지는 클라이언트 메모리/트래픽을 사용합니다. 미디어는 `preload=metadata`를 사용하지만 실제 범위·버퍼 크기는 브라우저가 결정합니다.

PDF.js는 Range와 화면 주변 페이지 렌더링을 사용하며 자동 선행 읽기를 억제합니다. 원본이 Range를 무시하거나 형식 특성상 필요하면 PDF 전체를 받을 수 있습니다. Canvas backing store는 페이지당 최대 4 megapixel (한 변 최대 4096 픽셀)로 제한하고 닫을 때 해제하지만 PDF 엔진 전체의 메모리/실행 시간 상한을 보증하지 않습니다. 텍스트는 첫 1 MiB만 요청하고 Range가 무시되어도 읽기를 중단합니다. 거대한 파일이나 신뢰할 수 없는 PDF를 처리하는 운영 환경에서는 별도의 자원/동시 요청 제한이 필요합니다.

## 업데이트 절차

`package.json` 버전은 범위가 아닌 정확한 버전입니다. `web/preview.js`의 `LIB` 경로도 함께 변경하고 `python3 tools/vendor.py --force`로 다시 준비하세요. 버전 경로가 어긋나면 installer는 실패합니다. `python3 tools/vendor.py --check`는 로컬 파일 해시를 검증하며 네트워크를 사용하지 않습니다.

`.github/dependabot.yml`에는 주간 npm 업데이트 제안 설정을 넣었습니다. 실제 GitHub 저장소에 올리고 Dependabot이 활성화되어야 동작하며, 자동 병합이나 런타임 최신 버전 로드는 하지 않습니다. 업데이트 PR은 위 경로 동기화와 실브라우저 검사를 거쳐 적용하세요.

## 검증 구분

2026-09-12: 공식 패키지를 수집하고 실제 PDF.js worker를 사용하는 연속 스크롤을
Chromium/WebKit에서 proxy 및 presigned 모드로 검증했습니다. Chromium에서는 Media Chrome
오디오도 확인했습니다. HTML은 두 브라우저에서 스크립트/외부 리소스 설정의 네 조합을
앱 미리보기와 새 탭 모두 검사했습니다. 모바일 빈 목록 너비와 확대/닫기 처리는 DOM 검사로 확인했습니다.
아이폰 실기기, WebKit 미디어 재생, 운영 저장소는 이번 검증 범위에 포함되지 않습니다.

`python3 tests/preview_e2e.py`는 Chromium,
`MORI_TEST_BROWSER=webkit python3 tests/preview_e2e.py`는 WebKit 검사입니다.

### HTML 렌더링 및 PDF 스크롤

`BROWSER_HTML_PREVIEW_ENABLED=true`를 설정하고 서버를 재시작하면 `.html`/`.htm`을
미리보기와 새 탭에서 HTML로 렌더링합니다. 기본값 `false`에서는 소스 텍스트로 표시합니다.
HTML 렌더링은 sandbox로 격리되며 인라인 CSS와 data 이미지/폰트를 지원합니다.
`BROWSER_HTML_PREVIEW_SCRIPTS=true`로 스크립트 실행을,
`BROWSER_HTML_PREVIEW_EXTERNAL_RESOURCES=true`로 HTTP(S) 외부 리소스 로드를 허용할 수 있습니다.
두 옵션의 기본값은 `false`이며 미리보기와 새 탭에 동일하게 적용됩니다.
외부 스크립트는 두 옵션을 모두 켜야 합니다. sandbox 격리는 유지되며 폼 제출은 차단합니다.
외부 리소스는 절대 URL을 사용해야 하며, 저장소 내 상대경로 리소스 연결은 지원하지 않습니다. HTML은 presigned 설정에서도
보안 응답 헤더를 적용하기 위해 proxy로 전달합니다. 다운로드는 기존 첨부파일 동작을 유지합니다.
HTML은 별도 문서로 로드하며 텍스트 미리보기의 1 MiB 제한을 적용하지 않습니다.

PDF는 페이지를 세로로 연속 스크롤하며 화면 주변 페이지만 렌더링합니다.
모바일에서도 새 탭 버튼을 표시하고, 모든 미리보기에서 하단 기술 정보를 생략합니다.


### iOS 18 PDF 호환성

PDF.js 6.3.289의 `getTextContent()`는 iOS 18에 없는 `ReadableStream` 비동기
이터레이터를 사용합니다. 앱은 `streamTextContent().getReader()`로 텍스트를 읽어
이 의존성을 피하고, 텍스트 추출 실패가 이미 그린 페이지를 오류 화면으로 덮지 않도록 처리합니다.
[PDF.js Safari 오류 보고](https://github.com/mozilla/pdf.js/issues/20973).

새 탭 PDF는 `Content-Type: application/pdf`와 `Content-Disposition: inline`으로,
다운로드는 `attachment`로 전달합니다. S3 presigned URL에도 같은 응답 값을 서명합니다.
일부 WebKit의 기본 PDF 뷰어와 충돌하는 CSP `sandbox`는 인라인 PDF 응답에만 생략하며,
HTML/SVG/텍스트의 격리는 유지합니다.
[WebKit 수정 기록](https://webkit.org/blog/16445/release-notes-for-safari-technology-preview-212/).
