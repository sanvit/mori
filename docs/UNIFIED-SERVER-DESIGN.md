# mori + s3-proxy 통합 설계

상태: 구현 전 설계. 2026-09-12 소스·설정·테스트 대조 기준.

목표는 하나의 Go 실행 파일, 프로세스, HTTP 리스너에서 `browser`, `spa`, `direct` 모드를 제공하고, S3·WebDAV·FTP/FTPS·SFTP가 같은 객체 캐시를 사용하는 것이다. 사용자는 저장소 자격 증명 없이 파일 URL로 접근한다. 서버 접근 인증은 별도 설정이다.

`100% 유지`의 완료 기준은 아래 기능·설정·검증 표에 누락이 없고 통합 구현에서 검증을 통과하는 것이다. 아직 통합을 구현하거나 그 결과를 검증했다는 뜻은 아니다. 모드 선택에 따른 기능 비활성화, 요청한 API 주소 변경, 원본 프로토콜의 한계는 명시적으로 구분한다.

## 1. 대조한 소스

현재 작업 트리의 mori와 [s3-proxy](https://github.com/sanvit/s3-proxy)를 대조했다. README, 환경변수, 서버·저장소·캐시 구현, UI·뷰어·ZIP, 테스트 목록과 주요 구현, Docker·배포 설정을 확인했다.

[mori 설정](../.env.example), [미리보기 명세](PREVIEW.md), [검증 범위](VERIFICATION.md)를 참고한다. 기존 검증은 새 통합 구현의 통과 증거로 재사용하지 않는다.

## 2. 모드와 URL 계약

### 2.1 라우팅

`SERVE_MODE=browser|spa|direct`. 미설정 기본값은 `browser`다. 모드는 시작 시 결정하고 실행 중 변경하지 않는다. 한 프로세스는 기존처럼 하나의 저장소·공개 루트를 선택한다.

| 요청 | browser | spa | direct |
|---|---|---|---|
| `/` | mori 파일 브라우저 | `INDEX_DOCUMENT` 해석 후 파일, 없으면 SPA 조건 검사 | `INDEX_DOCUMENT` 해석 후 파일, fallback 없음 |
| `/#/docs/` | 기존 폴더 탐색. fragment는 서버에 전달되지 않음 | 사이트 자체 fragment | 사이트/클라이언트 자체 fragment |
| `/docs/a.pdf` | 객체 전달, browser 표시 정책 | 객체 전달, 사이트 표시 정책 | 객체 전달, 사이트 표시 정책 |
| `/docs/` | 기본은 정확한 키 조회. `INDEX_DOCUMENT`를 명시하면 인덱스 해석 | `/docs/index.html` 등 인덱스 해석 | 인덱스 설정에 따라 해석 |
| `/dashboard`가 없고 HTML 탐색 요청 | 404 | `SPA_INDEX`로 fallback, 성공 시 200 | 404 |
| `/missing.js`가 없음 | 404 | 기본 404 | 404 |
| `/_mori/api/config`, `list`, `preview`, `archive` | 제공 | 로컬 404 | 로컬 404 |
| `/_mori/api/object?key=…` | 기존 객체 API | 정확한 객체 읽기만 제공 | 정확한 객체 읽기만 제공 |
| `/_mori/assets/…`, `/_mori/vendor/…` | 내장 UI 리소스 | 로컬 404 | 로컬 404 |
| `/_mori/healthz` | 로컬 상태 검사 | 동일 | 동일 |
| `/api/…`, `/app.js`, `/vendor/…`, `/index.html` | 저장소 경로 | 저장소 경로 | 저장소 경로 |

`/_mori/api/object`를 모든 모드에 두는 이유는 예약 경로와 겹치는 저장소 파일도 명시적 key로 읽을 수 있게 하기 위해서다. SPA/direct에서는 목록·ZIP·뷰어 설정 API를 활성화하지 않는다. 객체 API에도 동일한 인증·공개 루트 제한을 적용하고, SPA/인덱스/오류 페이지 대체를 적용하지 않는다.

`/_mori`와 `/_mori/` 전체는 프로그램 예약 영역이다. 미등록 경로는 로컬 404이며 원본 조회나 SPA fallback을 하지 않는다. 예를 들어 저장소의 `_mori/a.txt`는 `/_mori/api/object?key=_mori%2Fa.txt`로 받을 수 있다. 같은 주소가 앱 API이면서 원본 파일인 동작은 불가능하므로, 예약 이름과 충돌하는 사이트 리소스에는 별도 파일명/사이트 경로가 필요하다. 이 경계는 문서와 테스트에 포함한다.

browser의 `/index.html`은 이제 저장소 객체다. 기존 UI 별칭은 `/`로 정리한다. `/docs`를 `/docs/`로 자동 변경하기 위해 매번 저장소 목록을 조회하지 않는다. browser 폴더 탐색은 기존 hash URL을 유지한다.

### 2.2 API 이전 범위

| 기존 주소 | 새 주소 | 유지하는 계약 |
|---|---|---|
| `/api/config` | `/_mori/api/config` | 기존 필드 유지, `serveMode`, `cacheMode` 등 추가 필드만 허용 |
| `/api/list` | `/_mori/api/list` | prefix/cursor/refresh, JSON 구조, 오류, `X-Listing-Cache` |
| `/api/preview` | `/_mori/api/preview` | descriptor, 파일 읽기 없는 URL 발급, HTML 옵션, presigned 처리 |
| `/api/object` | `/_mori/api/object` | key/download, GET·HEAD, 조건부 요청, 다운로드·미리보기 전달 모드 |
| `/api/archive` | `/_mori/api/archive` | POST 준비, GET 일회용 스트리밍, 오류 코드·한도 |
| `/app.js` 등 UI 파일 | `/_mori/assets/app.js` 등 | MIME·ETag·인증·임베드 방식 |
| `/vendor/…` | `/_mori/vendor/…` | worker/CMap/WASM/폰트 경로, immutable 캐시 |
| `/healthz` | 기본 `/_mori/healthz` | 상태 검사 기능. `HEALTH_PATH=/healthz`로 기존 probe 호환 가능 |

구 API alias와 리다이렉트는 기본으로 두지 않는다. `/api/`를 저장소 경로로 사용할 수 있어야 한다. 외부 API 클라이언트는 base URL 변경이 필요하며 이는 이번에 요청한 명시적 변경이다. POST ZIP도 새 주소를 직접 호출한다.

서버 라우트, HTML 링크, 두 JS의 API URL, preview의 proxy URL 검증, ZIP 응답 URL, PDF worker·CMap·WASM·폰트·Media Chrome 경로, `tools/vendor.py`의 경로 검증, 테스트 fixture, README·예제·Docker healthcheck를 함께 이전한다. UI 코드에서는 공통 route 상수/URL 생성 함수를 사용한다. 디스크의 `web/vendor` 디렉터리를 바꿀 필요는 없다.

### 2.3 모드와 전달 방식의 구분

- `SERVE_MODE`는 URL과 화면의 역할이다. `direct`도 파일 본문은 mori를 거친다.
- `BROWSER_DOWNLOAD_MODE`와 `BROWSER_PREVIEW_MODE`는 browser UI/객체 API의 `proxy|presigned` 선택이다. 둘의 독립 설정을 유지한다.
- `/파일경로` 요청은 모든 모드에서 서버 경유다. browser의 presigned 옵션이 이 주소를 원본 리다이렉트로 바꾸지 않는다.
- S3 GET presign은 기존대로 지원한다. HEAD·목록·ZIP은 서버 경유다. WebDAV·FTP·SFTP는 presign 대상이 아니다.
- ZIP 자체는 동적으로 생성하므로 객체 캐시에 저장하지 않는다. ZIP 구성 파일은 공통 캐시에서 읽는다.

## 3. 내부 구조

```text
HTTP 요청
  → 경로 검증 / 로컬 상태 검사 / 인증
  → 모드별 라우터
      ├─ browser UI + 목록·preview·ZIP API
      ├─ 정확한 객체 API (fallback 없음)
      └─ 파일 경로 → 디렉터리 인덱스 → SPA 조건 → 사용자 오류 페이지
  → 객체 서비스 (조건부 요청·Range·메타데이터·내용 식별자)
      ├─ CACHE_MODE=internal → 공통 세그먼트 캐시
      ├─ CACHE_MODE=off      → 원본 스트리밍
      └─ CACHE_MODE=external → 기존 S3 외부 프록시 연결
  → S3 / WebDAV / FTP·FTPS / SFTP 어댑터
```

목록은 `List/Walk` 경로와 기존 메모리 캐시를 사용한다. presigned GET은 URL 발급 후 사용자가 S3에 접속한다. 내장 객체 캐시는 HTTP loopback이나 두 번째 listener 없이 함수/스트림으로 연결한다. 응답 전체를 `httptest.ResponseRecorder` 같은 메모리 버퍼에 넣는 연결은 사용하지 않는다.

제안 패키지:

| 위치 | 책임 |
|---|---|
| `internal/backend` | 기존 List/Walk/Stat/Open + 객체 메타데이터·내용 식별자 조건·백엔드 capability |
| `internal/s3`, `internal/backend/{webdav,ftp,sftp}` | 인증, 경로 변환, 원본 읽기·조건 검증 |
| `internal/objectcache` | upstream의 디스크 저장, eviction, 정책, fill 공유, 구간 스케줄링 |
| `internal/object` | 공통 객체 읽기, 기대 내용 식별자 확인, 캐시/원본 선택 |
| `internal/server` | 모드 라우팅, 인증, UI/API, HTTP 조건 처리, 표시·fallback 정책 |
| `internal/cache` | 기존 폴더 목록 메모리 캐시 |
| `cmd/mori` | 설정·생명주기·healthcheck 서브커맨드·단일 서버 |

기존 `*s3.Origin` 타입 단언으로 presign/ZIP 경로를 선택하는 구조는 capability로 바꾼다. 캐시 wrapper를 씌웠다는 이유로 S3 인식과 presign이 사라지면 안 된다. Presigner와 원본 메타데이터 조회는 캐시와 별도의 명시적 의존성으로 주입한다.

### 3.1 기존 인터페이스에 필요한 보강

현재 `Open(ctx,key,offset,length)`만으로는 요청한 내용 식별자와 실제 읽은 내용 식별자, HTTP 캐시 헤더, Range 원본 응답을 전달할 수 없다. 단순 wrapper만으로 통합을 완료하지 않는다.

객체 읽기 계약에는 아래 정보를 포함한다. 구체적 Go 타입 이름은 구현 단계에서 정하되 의미는 유지한다.

| 항목 | 내용 |
|---|---|
| 객체 메타데이터 | size, modified, native ETag, 내부 내용 식별자, validator 강도, MIME, Cache-Control, Expires, Content-Disposition, Content-Encoding |
| 메타데이터 조회 옵션 | cached/fresh, 재검증용 validator. ZIP 계획은 fresh |
| 읽기 옵션 | 정확한 offset/length, 기대 내용 식별자, native 조건부 읽기용 ETag, 취소 context |
| 읽기 결과 | 실제 메타데이터, 구간/길이 검증 결과, 닫을 수 있는 스트림. 뒤늦은 오류는 Read/완료 검증으로 전파 |
| capability | native 조건부 읽기, random read, HTTP multi-range passthrough, presign, native HTTP metadata |
| 원본 오류 | 없는 파일, 접근 거부, 내용 변경, 범위 오류, timeout, 기타 원본 실패를 구분 |

합성 내용 식별자와 HTTP strong ETag를 동일시하지 않는다. 기존 FTP·SFTP의 JSON ETag 값은 호환용으로 유지할 수 있지만, 그 값이 원본의 원자적 내용 일치 보증인 것처럼 사용하지 않는다. 클라이언트의 조건부 요청은 공통 HTTP 계층에서 처리하고, 원본 조건부 읽기는 어댑터가 처리한다.

### 3.2 백엔드별 읽기

| 백엔드 | 메타데이터/구간 읽기 | 내용 일관성 | 유지·보강할 점 |
|---|---|---|---|
| S3 | HEAD + Range GET | native ETag + If-Match, 응답 ETag/범위/길이 검증 | SigV4·익명·세션 토큰·path/virtual-host style·prefix·presign 유지 |
| WebDAV | PROPFIND + 필요 시 HEAD, HTTP Range GET | strong ETag가 있으면 If-Match, 없으면 크기·수정 시각 | 현재 PROPFIND만으로 누락되는 HTTP 캐시 헤더를 수집; HEAD 미지원이면 GET 헤더에서 보강 |
| FTP/FTPS | MLST/MLSD 또는 LIST, REST/RETR | 전후 stat·전송 길이 비교 | MLST/LIST 시각 정밀도 통일, 기본 연결 풀 4개 유지; 잘린 전송의 제어 연결 재사용 여부 검증 |
| SFTP | stat/fstat + 구간 읽기 | 열린 handle의 전후 fstat 및 경로 재검사 | SSH 공유 연결·재접속·키 인증·known_hosts 유지; 요청 취소가 다른 요청의 공유 연결을 끊지 않게 처리 |

원본이 HTTP Range를 무시하는 기존 호환 동작을 유지한다. 200 전체 응답이면 매 세그먼트마다 처음부터 반복 다운로드하지 않고, 한 순차 스트림을 분할하거나 해당 요청을 BYPASS하며 필요한 앞부분을 건너뛴다. capability에 없는 FTP REST는 동일한 제한된 순차 대안을 적용한다. Range 지원 자체를 얻었다고 원본 트래픽 절감까지 보장하지 않는다.

FTP/SFTP에는 모든 서버에서 쓸 수 있는 원자적 If-Match가 없다. 전후 검사가 통과해도 같은 크기·시각의 변경을 놓칠 수 있다. 특히 FTP LIST는 분 단위일 수 있다. 이를 없애려면 원본 내용 식별자/스냅샷 또는 전체 내용 검증 등 별도 비용이 필요하다. 이 한계를 `100% 일관성 보장`으로 표현하지 않는다. 감지된 변경은 내용 식별자 무효화 후 실패하고 새 바이트와 기존 내용을 섞어 정상 완료하지 않는다.

## 4. 기능 보존 목록 — mori

각 ID는 구현 PR과 회귀 검사에서 추적한다. "유지"는 이 설계의 요구사항이다.

| ID | 보존 기능 | 적용/검증 지점 |
|---|---|---|
| M01 | `/` 파일 브라우저, 제목, breadcrumb, 상위 폴더, hash URL, 뒤로 가기 | browser UI, 기존 폴더 링크 재사용 |
| M02 | 한 단계 조회·수동 다음 페이지·폴더 우선 정렬·불러온 항목만 정렬 | S3 delimiter, cursor, 빈 목록/오류/취소. 전역 검색을 새로 만들지 않음 |
| M03 | 목록 TTL 30초·0으로 끄기·최대 512페이지·동시 조회 공유 | 새로고침은 목록 첫 페이지만 우회, 본문 캐시 삭제 아님 |
| M04 | 파일/폴더 선택·현재 표시 항목 전체 선택·이동 시 선택 해제 | ZIP 비활성화 시 선택 UI와 API 제거 |
| M05 | 이미지 확대·맞춤, 오디오/영상 재생·탐색·속도·전체 화면/PiP | 기존 형식 판정과 플랫폼 제한 유지 |
| M06 | PDF 연속 스크롤·주변 페이지 렌더·확대·암호·취소 | 실제 PDF.js worker, CMap/WASM/폰트 새 URL, iOS 스트림 호환 수정 유지 |
| M07 | 텍스트 첫 1 MiB·줄바꿈·안전한 원문 표시 | Range 무시에도 클라이언트 읽기 중단; HTML/SVG 기본 원문 |
| M08 | HTML 렌더링 opt-in·스크립트/외부 리소스 개별 옵션 | 미리보기와 새 탭 동일 sandbox, HTML proxy 강제, 상대 리소스 미지원 한계 유지 |
| M09 | 새 탭/수정키/가운데 클릭·원본 열기·첨부 다운로드 | PDF inline/attachment 및 한글 파일명 유지 |
| M10 | 모바일 레이아웃·전체 화면 미리보기·하단 선택 바·safe area | 320/360/390/768px, 빈 목록, 터치·포커스·Esc·뒤로 가기 |
| M11 | 미리보기 이전/다음·재시도·오류 표시·지연 로딩 | 현재 불러온 파일만 대상; 닫기/전환 시 fetch/미디어/PDF 해제 |
| M12 | Media Chrome·PDF.js 수집·무결성·임베드 | 실행 시 CDN 불필요; vendor 누락 경고/native media fallback 유지 |
| M13 | 다운로드/미리보기의 독립 proxy/presigned 설정 | 4가지 조합, GET만 presign, client query로 모드 변경 불가 |
| M14 | presign TTL·공개 endpoint·최종 host 서명·응답 MIME/Disposition | 키·prefix·세션 토큰·CORS, 만료 시 자동 proxy 전환 없음 |
| M15 | preview descriptor는 HEAD/list/body 없이 생성 | API 응답 private/no-store, URL을 history/localStorage에 저장하지 않음 |
| M16 | 선택 폴더 재귀 ZIP·전체 페이지·빈 폴더·상대 경로 | 개별 HEAD/재귀 LIST로 계획, 계획과 같은 내용으로 파일 읽기 |
| M17 | ZIP Store·CRC·순차 스트리밍·네이티브 다운로드 | ZIP 전체 RAM/임시파일/JS Blob 없음, ZIP 자체 Range 미지원 |
| M18 | ZIP 한도·준비 시간·동시성·일회용 token·CSRF 검사 | 기본 200개/20GiB/동시2, 준비60초, token2분, 128계획/20,000항목 |
| M19 | ZIP 변경/삭제/길이 오류/취소·응답 시작 후 중단 | 부분 ZIP을 성공적으로 finalize하지 않음; 계획은 저장소 스냅샷 아님 |
| M20 | S3·WebDAV·FTP/FTPS·SFTP 기존 연결/목록/읽기 | WebDAV Basic/href 검증, FTP 익명/FTPS/MLSD·LIST, SFTP 비밀번호/키/호스트 키 |
| M21 | STORAGE_BASE_PATH와 기존 저장소 루트 결합 | 범위 밖 경로 거부, base path를 UI/API/ZIP 파일명에 노출하지 않음 |
| M22 | 인증 기본 비공개·명시적 공개·읽기 전용·안전한 오류 | 모든 cache HIT도 인증 후 처리; POST는 browser ZIP 준비만 허용 |
| M23 | GET/HEAD·Range·조건부 요청·실시간 스트리밍·취소 | 인증·Content-Type·CSP·origin 헤더 allowlist 유지 |
| M24 | standalone·외부 S3 proxy·.env·Go binary·Docker·배포 산출물 | 외부 캐시는 선택사항으로 보존; Linux/macOS/Windows, amd64/arm64 빌드 |

각 미리보기 형식의 전체 확장자 목록과 화면 동작은 `internal/media`, `docs/PREVIEW.md`, 기존 UI/DOM/E2E 테스트를 보존 기준으로 사용한다. 검색·업로드·삭제·변환·Office 뷰어·완성 ZIP 이어받기 등 원래 없는 기능은 통합 완료 조건에 추가하지 않는다.

## 5. 기능 보존 목록 — s3-proxy

근거: [proxy.go](https://github.com/sanvit/s3-proxy/blob/master/proxy.go), [segments.go](https://github.com/sanvit/s3-proxy/blob/master/segments.go), [cache.go](https://github.com/sanvit/s3-proxy/blob/master/cache.go), [cachecontrol.go](https://github.com/sanvit/s3-proxy/blob/master/cachecontrol.go), [config.go](https://github.com/sanvit/s3-proxy/blob/master/config.go).

| ID | 보존 기능 | 통합 후 계약 |
|---|---|---|
| P01 | S3 HTTP(S), SigV4/anonymous, bucket/region/prefix/session token | 기존 S3 연결을 보존하고 다른 저장소 adapter에도 캐시 제공 |
| P02 | 메타데이터 TTL·만료 후 ETag 재검증 | 304는 기존 세그먼트 재사용, 메타데이터 최신화 |
| P03 | Cache-Control 정책 | s-maxage/max-age/no-cache/private/no-store, respect-origin 스위치 |
| P04 | 경로별 캐시 규칙 | prefix/suffix/catch-all, 첫 일치, TTL/browser_ttl/bypass/ignore_query |
| P05 | 쿼리 cache key | sort/include/ignore, sort는 중복 값까지 정렬; 쿼리는 원본 객체 key에 붙이지 않음 |
| P06 | 기본 8 MiB block / 2 MiB segment | block 관리·삭제, segment 읽기·저장, sparse cache |
| P07 | 전체 GET·single Range의 부분 HIT 재사용 | 요청과 겹치는 누락 segment만 채움, 완전 HIT는 디스크에서 전달 |
| P08 | 인접 MISS를 제한된 fetch run으로 묶기 | `ORIGIN_FETCH_MAX_SIZE=16MiB`; HIT 구간을 건너뛰고 분리된 run은 병렬 가능 |
| P09 | 요청별 bounded window·전역 fill 제한 | window4, active origin run32 기본; queued cancel·느린 reader 제어 |
| P10 | 진행 중 fill 공유 | 부분적으로 겹치는 요청도 segment 단위 공유, 구독자 하나 취소 시 나머지는 계속 |
| P11 | 첫 바이트 스트리밍·중단 정리 | 전체 파일/segment 완료를 기다리지 않음; 완료 segment 유지, 미완료 폐기 |
| P12 | 디스크 용량 제한·block 단위 eviction | 실제 segment 바이트 집계, probation/protected, protected 예산80%, 대형 순차 읽기 보호 |
| P13 | 재시작·외부 파일 소실 복구 | 디스크 재집계, 재시작 후 probation, 사라진 block 잊기 및 재읽기 |
| P14 | 객체 변경 전후 격리 | ETag/size/modified 기반 내용 식별자, 변경 시 이전 segment 접근 차단·후속 eviction |
| P15 | 원본 구간 검증 | If-Match/응답 ETag/Content-Range/Content-Length/실제 길이, 초과·단축 응답 거부 |
| P16 | 완전히 캐시된 파일의 stale-if-error | 기간 내 원본 네트워크/5xx 실패에만 허용; 읽기 범위·조건·권한 유지 |
| P17 | 404/403 negative metadata | 기본 30초/10초, 오류 body는 저장하지 않음, 5xx 제외, 최대4096·만료·재시작 정리 |
| P18 | trailing-slash 인덱스 | `INDEX_DOCUMENT`, 비어 있으면 off, `/docs` 자동 목록조회 없음 |
| P19 | SPA fallback | 원본404 + HTML/document GET/HEAD; 기본 무확장자/`.html`, dotted route 옵션 보존 |
| P20 | custom 404와 400–599 오류 페이지 | 원래 상태 유지, 오류 객체 실패 시 내장 HTML, 재귀 fallback/부분 body 덧붙이기 금지 |
| P21 | multi-range 원본 BYPASS | S3/지원 HTTP 원본은 multipart 응답 스트리밍, segment 캐시 없음 |
| P22 | 헤더·조건부 요청 | X-Cache 7종, Age, ETag, Last-Modified, Accept-Ranges, Cache-Control, Expires |
| P23 | health·access log·graceful shutdown | cache bytes/max/blocks, 로그 method/path/status/bytes/cache/duration, query 제외, in-flight drain |
| P24 | 설정·단위·설정 오류 검증 | 기존 CACHE/ORIGIN/INDEX/SPA/ERROR/HEALTH ENV 이름·단위·빈 값 semantics 보존 |

P19의 dotted route 옵션을 켜면 확장자 있는 경로도 HTML 탐색 조건에서 fallback할 수 있다. 따라서 “누락된 JS는 항상 404”가 아니라 기본값에서의 동작이다. P21은 FTP/SFTP에 native HTTP multipart가 없으므로 기존처럼 multi-range 416을 유지한다. single Range 캐시는 모든 백엔드에 제공한다.

## 6. 캐시와 HTTP 정책의 경계

### 6.1 같은 파일의 캐시 공유

캐시 식별자는 저장소 종류 + endpoint/계정의 비밀이 아닌 식별자 + bucket/원본 root + 공개 base path + 상대 key + 정규화한 객체 query + 객체 내용 식별자 + 캐시 포맷/segment 크기를 포함한다. 비밀번호/비밀 키/token은 경로나 로그에 넣지 않는다. 계정 변경과 cache namespace 변경을 명시적으로 반영하며, namespace가 같아도 프로세스 시작 후 메타데이터를 재검증한 뒤 기존 바이트를 사용한다.

`/movie.mp4`, `/_mori/api/object?key=movie.mp4`, 미리보기, ZIP은 객체 query가 같으면 같은 segment를 사용한다. 내부 제어 query인 key/download/ZIP token과 UI 라우트는 cache key에 들어가지 않는다. 일반 파일 URL의 query는 기존 query policy로 처리하며 사용자 임의 query를 저장소 자격 증명이나 임의 원본 URL로 해석하지 않는다.

캐시에는 원본 바이트와 원본 메타데이터만 저장한다. inline/attachment, browser 텍스트 표시, SPA HTML, 오류 상태, 인증 결과를 바이트 캐시에 합치지 않는다. 인덱스와 SPA fallback은 해석된 실제 key로 캐시를 공유하지만 외부 요청의 응답 상태는 독립적으로 결정한다. 존재하지 않는 `/dashboard`의 negative 항목과 실제 `/index.html` 항목도 분리한다.

규칙은 앱 API 주소가 아니라 해석된 파일 경로 기준이다. 기존 `S3_PREFIX`를 제외하고 `STORAGE_BASE_PATH`를 포함한 규칙 경로를 유지하여 현재 외부 proxy 구성의 `/base/assets/` 규칙이 달라지지 않게 한다. 이 내부 경로를 로그/UI 응답으로 노출하지 않는다. 설정 변경 시 TTL/규칙/browser_ttl은 현재 정책으로 다시 평가한다.

### 6.2 응답 표시와 캐시 헤더

| 응답 종류 | 표시·정책 |
|---|---|
| browser UI/API·ZIP | 기존 CSP/CSRF/인증, private/no-store. vendor는 기존 private immutable |
| browser 객체 API·파일 URL | 기존 안전 MIME/Disposition/HTML sandbox/PDF 예외 유지. 사용자 콘텐츠가 앱의 권한으로 실행되지 않도록 함 |
| spa/direct 파일 URL | 사이트용 MIME로 HTML·JS·CSS 실행 가능, 브라우저 preview sandbox를 적용하지 않음. 원본/경로 규칙의 HTTP 캐시 헤더 사용 |
| 모든 모드의 정확한 객체 API | 파일 데이터/조건 처리만. fallback 없음, 안전한 파일 표시. browser에서만 기존 presigned/preview 설정 적용 |
| custom 오류 페이지 | 원래 오류 상태와 해당 모드의 표시 정책. browser API의 JSON 오류와 ZIP 본문에는 적용하지 않음 |

사이트 HTML을 실행하는 spa/direct에는 browser의 listing/ZIP API를 등록하지 않는다. 경로 prefix는 origin 격리가 아니므로, browser 모드에서 저장소 HTML에 사이트 모드 정책을 재사용하지 않는다.

디스크 TTL과 브라우저 HTTP 캐시 TTL은 분리한다. browser의 private/no-store 헤더 때문에 서버 내부의 모든 캐시가 BYPASS되는 일이 없어야 한다. 반대로 `browser_ttl` 때문에 인증된 browser/API 응답이 public으로 바뀌어서는 안 된다. spa/direct의 공개 파일에서 기존 browser_ttl을 유지하며, 인증된 파일 응답은 private으로 제한한다. 0초 지정도 “미지정”과 구분한다.

HTTP 원본에는 자동 decompression을 끄고 바이트/Content-Encoding 일관성을 보장한다. HTTP 헤더는 allowlist만 전달하고 원본 Set-Cookie, hop-by-hop, 브라우저 Authorization을 원본 응답/요청에 섞지 않는다. FTP/SFTP의 MIME는 경로로 판별하고, Cache-Control은 기본값/경로 규칙으로 생성한다.

### 6.3 조건부 읽기·실패 동작

공통 계층에서 If-Match, If-None-Match, If-Modified-Since, If-Unmodified-Since, If-Range를 처리한다. strong/weak 비교와 우선순위를 구분한다. 304/412/416, HEAD와 0바이트 파일, suffix/open-ended Range는 캐시 상태와 무관하게 일관되어야 한다. ZIP의 기대 내용 식별자은 fresh 원본 메타데이터 및 읽기 결과와 검증하고 stale fallback을 허용하지 않는다.

stale-if-error는 완전히 캐시된 같은 내용 식별자만 대상으로 한다. Range 요청에는 요청한 범위, HEAD에는 본문 없음, 조건 불일치에는 412 등 원래 계약을 지킨다. 인증 실패와 원본403/404를 stale 성공으로 바꾸지 않는다. 원본 접근권한 변경의 감지 지연은 메타데이터 TTL만큼 있을 수 있으므로 즉시 재검증이 필요한 경로는 TTL0/BYPASS로 설정한다.

응답 전 실패는 올바른 오류 상태로 반환하고, 응답 시작 후에는 스트림을 실패시킨다. HTTP/2에서도 성공 EOF로 보이지 않게 하고 오류 HTML/JSON을 파일 끝에 붙이지 않는다. 캐시 쓰기 실패 시 미완료 segment를 publish하지 않는다. 응답 전이면 실패를 보고하고, 응답 후이면 중단하는 정책을 기본으로 한다. 운영자가 `CACHE_MODE=off`로 캐시 없이 실행할 수 있다.

### 6.4 upstream 동작을 그대로 복사하면 생기는 문제

아래는 코드 검토에서 확인한 통합 시 수정 대상이며, 그 동작을 보존 요구사항으로 삼지 않는다. 이번 문서 작성에서 재현 테스트를 실행한 것은 아니다.

- upstream 객체 계층은 client If-Match/If-Range를 완전히 처리하지 않는다. mori의 ZIP·조건부 다운로드 보존을 위해 공통 계층에서 처리해야 한다.
- upstream stale 경로는 `serveCachedFull`로 들어가 Range 요청에도 전체 파일을 선택할 수 있다. 조건·Range 처리를 먼저 거친다.
- upstream BYPASS 함수는 HEAD에도 원본 GET을 호출한다. 통합판 HEAD는 객체 본문을 읽지 않는다.
- 원본304에서 일부 헤더만 돌아오면 기존 Cache-Control을 잃을 수 있다. 메타데이터 병합 규칙을 둔다.
- custom 오류/SPA 내부 요청은 외부 파일의 Range·조건 헤더를 그대로 물려받지 않는다. 대체 문서의 전체 메타데이터와 상태를 따로 처리한다.
- 모듈 전역 정책 변경 후 저장된 `Cacheable` 값만 신뢰하지 않는다. 전역 off는 경로 TTL보다 우선한다. 경로 TTL의 원본 no-store/private override 기능은 명시적 설정으로 보존한다.
- `ignore_query` 선택은 현 코드에서 TTL의 첫 일치 규칙과 다른 결과가 날 수 있다. 규칙의 첫 일치 계약으로 통일하고 기존의 다중 규칙 겹침 사례를 migration note에 기록한다.
- `path.Clean`으로 잘못된 경로를 다른 파일로 바꾸지 않는다. dot segment/제어 문자/역슬래시 등 기존 mori 검증을 유지하되 `a..b.txt`처럼 점이 포함된 정상 파일은 허용한다. URL decode는 한 번만 수행한다.

## 7. 설정 호환성

### 7.1 새 설정과 우선순위

| 설정 | 제안 계약 |
|---|---|
| `SERVE_MODE` | browser/spa/direct. 미설정 browser; 미설정 상태에서 legacy SPA_MODE=true이면 spa |
| `CACHE_MODE` | off/internal/external. 미설정 시 BROWSER_PROXY_URL이 있으면 external, 없으면 off로 기존 standalone 동작 보존 |
| `CACHE_ENABLED` | 선택된 캐시의 enable 스위치. false면 본문·metadata/negative cache까지 off, 디스크 쓰기 없음 |
| `LISTEN_ADDR` | 공통 listen. 미설정이면 BROWSER_LISTEN_ADDR, 둘 다 없으면 :8080. 둘이 서로 다르게 명시되면 시작 오류 |
| `AUTH_MODE` | basic/public. 미설정이면 기존 BROWSER_USERNAME/PASSWORD/PUBLIC 규칙으로 판정 |
| `HEALTH_PATH` | 미설정 /_mori/healthz, 빈 값이면 HTTP probe off, 기존 /healthz 명시 가능 |

새 `.env.example`은 `SERVE_MODE=browser`, `CACHE_MODE=internal`, `CACHE_ENABLED=true`를 명시하여 통합 캐시를 바로 사용할 수 있게 한다. 외부 프록시용으로 사용하던 `CACHE_ENABLED=true`만으로 갑자기 /cache 디스크를 요구하지 않는다. 새 예제의 변경과 기존 설정을 읽을 때의 기본값을 구별한다.

`CACHE_MODE=external`은 S3만 허용하고 BROWSER_PROXY_URL이 필수다. `internal`과 외부 proxy URL이 함께 명시되면 시작 오류로 이중 캐시를 막는다. 모드와 `SPA_MODE`가 모순되면 시작 오류다. SPA_MODE=false만으로 direct를 추론하지 않는다. 기존 mori 배포에서도 해당 값을 사용했기 때문이다. upstream 이용자는 SERVE_MODE=direct 또는 spa 및 AUTH_MODE=public을 명시하면 기존 공개 파일 제공을 재현할 수 있다.

AUTH_MODE를 명시하면 legacy 공개 여부보다 우선한다. basic은 사용자명/비밀번호가 모두 필요하고 public은 저장소 자격 증명과 무관하게 서버 파일 접근을 공개한다. 미설정 때에는 두 값이 있으면 Basic 인증을 적용하는 기존 동작을 보존한다. 캐시 HIT를 포함해 인증은 파일을 읽기 전에 수행한다. HEALTH_PATH는 루트(`/`)나 등록된 API/리소스와 충돌하면 시작 오류로 처리하며, 별도 경로를 명시하면 해당 경로가 원본 파일보다 우선한다.

browser에서는 INDEX_DOCUMENT 기본이 빈 값, spa/direct에서는 index.html이다. 명시한 빈 값은 모든 모드에서 off로 유지한다. SPA_MODE=true가 있는 upstream 예제는 SERVE_MODE 없이도 spa로 인식한다. spa/direct에서 INDEX_DOCUMENT가 비어 있을 때 S3의 bare path/bucket HTTP GET 응답을 제공하던 기능은 S3 passthrough로 보존하되, 목록/XML 등 객체 메타데이터가 없는 응답은 segment 캐시에서 제외한다. 다른 저장소의 디렉터리는 목록 화면을 생성하지 않고 404로 응답한다.

S3 region/endpoint는 기존 mori 기본값을 유지한다. upstream의 us-east-1 기본값에 의존하던 이용자는 명시해야 한다. `.env` literal 처리·프로세스 ENV 우선·두 자격 증명 동시 설정 검증은 유지한다. 비활성 모드의 browser 전용 설정은 경고 후 사용하지 않으며, browser에서 지원되지 않는 presigned 백엔드는 기존처럼 시작 오류다.

### 7.2 통합 시 지원할 ENV 인벤토리

현재 `.env.example`은 실행 코드에서 사용하는 설정만 제공한다. 아래에는 향후 통합할 upstream 캐시 설정도 포함되어 있다.

| 그룹 | 유지할 이름 |
|---|---|
| 저장소 선택/공개 범위 | STORAGE_BACKEND, STORAGE_BASE_PATH |
| browser/인증/listen/list | BROWSER_TITLE, BROWSER_LISTEN_ADDR, BROWSER_PORT(Compose용), BROWSER_USERNAME, BROWSER_PASSWORD, BROWSER_PUBLIC, BROWSER_LIST_TTL |
| 전달 | BROWSER_PROXY_URL, BROWSER_DOWNLOAD_MODE, BROWSER_PREVIEW_MODE, BROWSER_PRESIGN_TTL, BROWSER_PRESIGN_ENDPOINT |
| HTML | BROWSER_HTML_PREVIEW_ENABLED, BROWSER_HTML_PREVIEW_SCRIPTS, BROWSER_HTML_PREVIEW_EXTERNAL_RESOURCES |
| ZIP | BROWSER_ZIP_ENABLED, BROWSER_ZIP_MAX_FILES, BROWSER_ZIP_MAX_SIZE, BROWSER_ZIP_CONCURRENCY |
| S3 | S3_ENDPOINT, S3_REGION, S3_BUCKET, S3_ACCESS_KEY_ID, S3_SECRET_ACCESS_KEY, S3_SESSION_TOKEN, S3_FORCE_PATH_STYLE, S3_PREFIX |
| WebDAV | WEBDAV_URL, WEBDAV_USERNAME, WEBDAV_PASSWORD |
| FTP/FTPS | FTP_ADDR, FTP_USERNAME, FTP_PASSWORD, FTP_TLS, FTP_PATH |
| SFTP | SFTP_ADDR, SFTP_USERNAME, SFTP_PASSWORD, SFTP_KEY_FILE, SFTP_KNOWN_HOSTS, SFTP_INSECURE_HOST_KEY, SFTP_PATH |
| cache | CACHE_ENABLED, CACHE_DIR, CACHE_BLOCK_SIZE, CACHE_SEGMENT_SIZE, CACHE_MAX_DISK_SIZE, CACHE_DOWNLOAD_CONCURRENCY, CACHE_QUERY_MODE |
| TTL/정책 | CACHE_DEFAULT_TTL, CACHE_MIN_TTL, CACHE_MAX_TTL, CACHE_RESPECT_ORIGIN, CACHE_STALE_IF_ERROR, CACHE_NEGATIVE_TTL_404, CACHE_NEGATIVE_TTL_403, CACHE_RULES_JSON |
| 원본 fetch | ORIGIN_FETCH_MAX_SIZE, ORIGIN_MAX_CONCURRENT_REQUESTS |
| 사이트 | INDEX_DOCUMENT, SPA_MODE, SPA_INDEX, SPA_ALLOW_DOTTED_ROUTES |
| 오류 페이지 | ERROR_PAGE_404, CUSTOM_404_PATH(alias), ERROR_PAGES_JSON |
| 운영 | LISTEN_ADDR, HEALTH_PATH, ACCESS_LOG, SHUTDOWN_TIMEOUT |

CACHE_RULES_JSON의 prefix/suffix/ttl/browser_ttl/bypass/ignore_query, byte 크기의 SI/IEC·소수 단위, cache duration의 d 단위도 보존한다. ZIP 크기/기간의 기존 별도 제약을 전역 parser 교체로 바꾸지 않는다. 오타 bool·음수·overflow·잘못된 duration·block/segment 배수 오류는 시작 시 검증한다. ERROR_PAGES_JSON의 동일 상태 설정이 ERROR_PAGE_404를 덮는 기존 우선순위도 유지한다.

설정 예시(나머지 저장소 ENV는 기존과 동일):

```dotenv
# 파일 브라우저 + 모든 지원 저장소의 내장 캐시
SERVE_MODE=browser
STORAGE_BACKEND=sftp
CACHE_MODE=internal
CACHE_ENABLED=true
CACHE_DIR=/cache
AUTH_MODE=basic
BROWSER_USERNAME=reader
BROWSER_PASSWORD=replace-with-your-password
```

```dotenv
# 사이트와 파일 URL을 공개, 원본 저장소 자격 증명은 서버에만 설정
SERVE_MODE=spa
AUTH_MODE=public
STORAGE_BACKEND=s3
CACHE_MODE=internal
INDEX_DOCUMENT=index.html
SPA_INDEX=/index.html
SPA_ALLOW_DOTTED_ROUTES=false
```

```dotenv
# 파일 URL 전용. 인덱스도 필요 없으면 빈 값
SERVE_MODE=direct
AUTH_MODE=public
STORAGE_BACKEND=webdav
CACHE_MODE=internal
INDEX_DOCUMENT=
```

## 8. 운영·디스크·종료

- 기본 Compose는 mori 하나와 객체 캐시 volume만 사용한다. non-root UID10001, 읽기 전용 root filesystem, 캐시 디렉터리 쓰기 권한을 Dockerfile과 release Dockerfile 양쪽에서 준비한다.
- 외부 캐시용 Compose 파일은 제공하지 않는다. 기존에 운영 중인 프록시는 BROWSER_PROXY_URL과 CACHE_MODE=external로 연결할 수 있다. 기본 통합 배포는 upstream을 별도 컨테이너로 빌드/실행하지 않는다.
- 캐시 포맷을 식별해 호환성을 검사하고 한 cache directory에 한 프로세스만 쓰도록 소유/잠금 계약을 둔다. 구 proxy volume을 즉시 새 namespace로 오인해서 사용하지 않는다. 기본은 별도 하위 directory에 새 캐시를 만들고 기존 데이터는 보존한다. warm cache 이전이 필요하면 저장소 identity와 segment 설정을 검증한 명시적 import 단계로 다룬다.
- protected tier는 재시작 시 probation으로 돌아가는 upstream 동작을 유지한다. 임시 미완료 파일·만료 negative metadata를 정리하고 정확한 디스크 사용량을 복구한다. 기존 `.blk` 잔재의 용량 집계도 migration 검증 대상이다.
- 전역 active origin run 제한은 cache fill에 적용하며 metadata/BYPASS는 기존처럼 별도다. browser의 64개 요청 슬롯과 ZIP 제한을 보존한다. FTP는 풀 크기에 맞춰 run을 제한하고 metadata 재검사가 data 연결 고갈에 막히지 않도록 예약/반환 순서를 설계한다.
- health는 `{status,cache_bytes,cache_max_bytes,cache_blocks}`를 유지하고 mode/backend/cache mode는 추가 가능하다. 단순 liveness이며 원본 접근 성공을 의미하지 않는다. HTTP probe가 꺼져도 작동하는 프로세스 검사용 방식을 구분한다.
- Docker healthcheck는 하드코딩된 /healthz 대신 설정을 읽는 `mori healthcheck`를 호출한다. HEALTH_PATH가 비어 있으면 local listener 연결 검사를 하고, 있으면 해당 HTTP 경로를 검사한다. LISTEN_ADDR의 wildcard/IPv6를 loopback 접속 주소로 변환한다. 포트 미개방과 응답 실패를 구분한다.
- access log는 query·Authorization·원본 비밀정보·공개 base path를 제외한다. health는 제외하고 UI/API/file 요청을 중복 기록하지 않는다. 파일 경로는 percent-encoded 형태로 남긴다.
- 종료는 listener drain → 진행 중 HTTP/ZIP 및 공유 fill 완료 대기 → 제한 시간 후 취소 → 캐시 임시파일/FTP pool/SFTP 연결 정리 순서다. main이 drain goroutine보다 먼저 반환하지 않게 한다. 종료 시간이 지나면 HTTP/2 스트림도 실패 처리한다.
- 빌드 시 viewer assets 수집과 기존 배포 플랫폼을 유지한다. 새 cache lock/rename/open-file eviction이 Windows에서도 동작하도록 OS별 차이를 검증한다. 포함하는 외부 코드의 원본 소유권·라이선스 표기는 해당 소스와 의존성에 유지한다.

## 9. 구현 순서와 검증 완료 기준

### 9.1 순서

1. 기존 테스트를 실행해 baseline을 기록하고, M/P 기능 ID와 테스트를 연결한다. 실패가 있으면 기존 실패와 통합 회귀를 구분한다.
2. 모드·라우팅·`/_mori/api`/assets/vendor 이전을 구현한다. 캐시 off에서 기존 browser 기능과 파일 URL, spa/direct 사이트 동작을 먼저 검증한다.
3. upstream 캐시를 internal 패키지로 추출하고 기존 테스트를 이식한다. S3 어댑터와 공통 객체 서비스를 연결해 내용 변경/조건부 요청/ZIP을 검증한다.
4. WebDAV·FTP/FTPS·SFTP에 metadata·내용 검증 읽기 계약을 구현하고 같은 segment 캐시를 연결한다. 원본 capability별 fallback을 검증한다.
5. external/off 호환, 설정 이전, 상태 검사·종료·Docker·배포 패키지·문서를 마무리한다.
6. 아래 교차 검증을 통과한 항목만 완료로 표시한다. 기능 ID가 빠진 상태로 통합 완료/100% 유지라고 보고하지 않는다.

### 9.2 검증 표

| 묶음 | 필수 사례 | 기존 근거/추가할 증거 |
|---|---|---|
| browser UI | 목록·선택·페이지·ZIP on/off·모바일·미리보기·새 탭·뒤로 가기 | ui_smoke, preview_dom, http_e2e |
| 실제 viewer | Chromium/WebKit의 PDF worker·Range·CMap/WASM 경로·Media Chrome·HTML 네 조합 | preview_e2e. 실제 asset 검증을 DOM 대역과 분리 |
| 주소 이전 | 모든 API/worker/token URL 새 prefix, 구 /api/app.js/vendor/index.html은 원본, 예약 충돌 객체는 key API | 라우터·frontend·fixture·문서 경로 대조 |
| 모드 | `/`, 파일, 디렉터리, 404 HTML/JSON, `.html`, dotted 옵션, API404와 fallback 우선순위 | 3모드 table tests + 실제 사이트 JS/CSS 실행 |
| 정확한 객체 | 없는 객체·ZIP·preview에 SPA/custom-error body가 들어가지 않음 | object API + archive tests |
| 저장소 | 3모드 × S3/WebDAV/FTP/FTPS/SFTP × internal/off | 기존 backend 통합 서버 + 실제 Apache/vsftpd/OpenSSH, FTPS TLS 연결 |
| 인증 | basic/public, HIT/MISS/STALE/negative 모두 접근 확인, prefix 탈출·한글·%·+·?·#·예약키 | config/path/header tests, 로그/API 비밀정보 미노출 |
| HTTP | GET/HEAD/OPTIONS·0바이트·Range suffix/open·304/412/416·If-Range/strong/weak | HTTP1.1/HTTP2, cache HIT/MISS/BYPASS/STALE 모두 확인 |
| cache policy | TTL origin/override/clamp, browser_ttl0, no-store/private/no-cache, 전역off, query 중복 정렬/규칙 우선순위 | upstream cachecontrol/config 검증 확대 |
| cache I/O | 부분 HIT·연속 MISS 합치기·최대 run·stream 첫 바이트·window·취소·겹치는 요청 | upstream segments tests + origin 요청 수/범위 기록 |
| 내용 변경 | HEAD→GET 사이 변경, ZIP 계획 후 변경, segment 경계 변경, late metadata race, 실제 길이 오류 | upstream 내용 변경 테스트 + 모든 backend 변경 주입 |
| 디스크 | eviction 보호 tier·cache보다 큰 파일·재시작·소실·디스크 오류·임시파일·namespace 변경 | upstream cache tests + 새 disk/migration tests |
| stale/negative | 기간 안/밖, 완전/부분 cache, 403/404/5xx, 새 파일 발견, 4096 budget, range stale | upstream negative tests + 범위/인증 회귀 |
| ZIP | 1,005개 이상 pagination·빈 폴더·경로/수/크기·token/CSRF·취소·불완전 ZIP | 기존 archive/recursive + internal/external/off에서 실행 |
| 전달 설정 | browser proxy/presigned 네 조합·public endpoint 서명·HTML proxy 강제 | 기존 botocore fixture/http_smoke, non-S3 지원 불가 검증 |
| 운영/배포 | race/vet·종료 drain·SFTP 공유 요청 취소·cache 권한·health 설정·release build | Go 검사, Docker 실제 기동, Linux/macOS/Windows 산출물 |

부하 검증은 단순 HIT 속도뿐 아니라 대용량 영상 탐색, 다수 느린 클라이언트, FTP 연결 수, origin fetch 횟수, RSS, 디스크 점유와 종료 시간을 기록한다. window × segment는 요청의 데이터 버퍼 근사치이며 전체 RSS의 상한이라고 표현하지 않는다.

100% 기능 유지 판정과 외부 환경 호환성 보장은 다르다. 실제 AWS/MinIO/R2·다양한 WebDAV·실기기·플랫폼 코덱 등 실행하지 못한 환경은 미검증으로 남기고 완료 항목과 구분한다. 이 설계 단계에서는 실행 코드 변경 및 통합 테스트 실행을 하지 않았다.
