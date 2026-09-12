# 구조

mori 실행 파일 하나가 파일 브라우저와 사이트 제공, 공통 디스크 캐시를 모두 실행한다. 별도의 캐시 프로세스나 리버스 프록시가 필요하지 않다. 캐시와 사이트 라우팅은 `internal/objectcache`에 있고 저장소 어댑터는 `internal/backend`에 있다.

이 문서의 목록은 기능 명세다. 모든 운영 환경의 성능·일관성을 보증한다는 뜻은 아니다. 실행한 검사와 실서버에서 확인하지 않은 범위는 [검증 기록](VERIFICATION.md)에 구분한다.

## 모드와 URL

| 경로/기능 | browser | spa | direct |
|---|---|---|---|
| `/` | 기존 파일 브라우저 | 원본 인덱스 | 원본 인덱스 |
| `/폴더/파일` | 안전한 파일 표시/다운로드 | 원본 사이트 응답 | 원본 사이트 응답 |
| `/_mori/api/config,list,preview,archive` | 기존 API 계약 유지 | 404 | 404 |
| `/_mori/api/object?key=…` | 정확한 객체, 기존 전달 설정 | 정확한 객체, 안전한 표시 | 정확한 객체, 안전한 표시 |
| `/_mori/assets/…`, `/_mori/vendor/…` | 내장 UI와 뷰어 | 404 | 404 |
| `/_mori/healthz` | 상태 검사 | 상태 검사 | 상태 검사 |
| SPA fallback | 없음 | HTML 탐색의 원본404에 적용 | 없음 |
| 사용자 오류 페이지 | 파일 경로에 적용 | 파일 경로에 적용 | 파일 경로에 적용 |

`/_mori/` 전체는 내부 예약 공간이다. 그 이름의 저장소 객체도 object API의 key로 읽을 수 있다. 이전 `/api/`, `/app.js`, `/vendor/`에는 alias나 redirect를 두지 않으며 이제 저장소 파일 경로다. ZIP token 검증과 뷰어 worker·CMap·WASM·폰트 URL도 함께 이전했다. 디스크의 `web/vendor` 위치는 그대로다.

방문자는 S3 키를 입력하지 않는다. 서버만 저장소 인증정보를 사용한다. mori Basic 인증은 별개이며, 공개 접근은 `BROWSER_PUBLIC=true` 또는 `AUTH_MODE=public`로 명시한다. `SERVE_MODE=direct`도 mori 경유 파일 제공이며 S3 presign과는 다르다.

browser의 기존 목록·선택·미리보기·ZIP을 유지한다. `BROWSER_DOWNLOAD_MODE`와 `BROWSER_PREVIEW_MODE`의 proxy/presigned 선택은 독립적이다. 파일 경로 URL은 항상 서버 경유이며, GET presign만 객체 캐시를 우회한다. HEAD·목록·ZIP은 서버 경유다. SPA/direct에는 저장소 HTML이 같은 origin에서 실행되므로 목록·ZIP API를 노출하지 않는다.

## 내부 연결

```text
단일 HTTP 서버 — 인증·모드별 라우터
├─ browser UI / 목록 / preview descriptor / S3 presign
├─ 파일 경로·object API ─┐
└─ ZIP 구성 파일 읽기 ──┴─ objectcache.Proxy
                          ├─ metadata / sparse segments / 보호 LRU
                          └─ BackendOrigin
                             ├─ 기존 S3 SigV4 HTTP
                             ├─ WebDAV HTTP / PROPFIND
                             └─ FTP·FTPS / SFTP Stat·Open
```

`server.Open`이 저장소별 캐시 namespace를 열고 공통 읽기 경로를 연결한다. 목록·재귀 조회·presign은 기존 저장소 인터페이스를 유지하며 본문 캐시와 분리한다. ZIP은 fresh 메타데이터로 계획을 확인하고 같은 캐시에서 구성 파일을 읽는다. 완성 ZIP 자체를 캐싱하지 않는다.

S3와 strong native validator가 있는 WebDAV는 If-Match와 응답 ETag·범위·길이를 검사한다. WebDAV의 목록·Stat·HEAD 식별자는 PROPFIND 메타데이터로 통일하고, 원본의 유효한 strong/weak ETag를 보존한다. weak/native 또는 합성 식별자는 native If-Match로 보내지 않고 Stat/Open/Stat 경로로 검사한다. HTTP 원본이 Range를 무시하면 제한된 탐지 후 해당 요청을 BYPASS하여 같은 파일을 세그먼트마다 처음부터 받지 않는다.

FTP/FTPS·SFTP와 ETag 없는 WebDAV는 저장소 식별값(종류·주소·사용자·루트), 전체 파일 경로, 크기, 수정시각을 SHA-256으로 조합한 `W/"mori-…"` 약한 ETag를 사용한다. 수정시각은 원본 정밀도를 유지하고 UTC로 정규화하며 접근시각은 제외한다. 읽기 전후 Stat과 실제 길이도 검사한다. 이는 원자적 스냅샷이나 내용 해시가 아니다. 같은 크기·시각의 덮어쓰기는 감지하지 못할 수 있다. 수정시각이 없으면 합성 ETag만으로 본문을 캐싱하거나 변경 없음 304를 판정하지 않는다(존재 여부를 확인하는 `If-None-Match: *`는 제외). FTP REST 미지원 서버에 임의 구간 읽기까지 보장하지 않는다. 감지된 변경은 캐시 식별자를 무효화하고 전송을 실패시킨다.

약한 ETag는 `If-None-Match` 재검증에 사용할 수 있지만 HTTP `If-Match`의 strong 비교를 통과하지 않는다. 약한 `If-Range`는 부분 응답 대신 전체 응답으로 처리하며, 조건 없는 Range는 계속 지원한다. ZIP은 HTTP strong 조건으로 위장하지 않는 내부 메타데이터 토큰으로 계획과 읽기를 비교한다. 위의 동일 크기·수정시각 한계는 ZIP에도 적용된다.

## 파일 브라우저 명세

| ID | 기능 | 계약과 검증 지점 |
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
| M24 | standalone·.env·Go binary·Docker·배포 산출물 | 캐시까지 한 프로세스에 포함; Linux/macOS/Windows, amd64/arm64 빌드 |

미리보기 형식별 확장자·동작·코덱 한계는 [미리보기 명세](PREVIEW.md)를 따른다. 검색·업로드·삭제·변환·Office 뷰어·완성 ZIP 이어받기는 원래 제공하지 않은 기능이다.

## 객체 캐시와 사이트 제공 명세

| ID | 기능 | 계약 |
|---|---|---|
| P01 | 저장소 독립 | 캐시는 `ObjectOrigin`(Head/GetConditional)만 사용하고 S3 SigV4·WebDAV·FTP·SFTP 연결은 각 adapter가 담당 |
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
| P24 | 설정·단위·설정 오류 검증 | CACHE/ORIGIN/INDEX/SPA/ERROR/HEALTH ENV의 단위와 빈 값 semantics, 시작 시 검증 |

캐시·프록시·세그먼트 단위 검사는 `internal/objectcache`에서 실행한다. 저장소와 묶은 검사는 `internal/server/unified_test.go`, `unified_http_test.go`, `internal/config/serving_test.go`와 실제 HTTP·브라우저·저장소 검사에 있다. 응답 도중 실패하면 오류 본문을 덧붙이지 않고 HTTP 스트림을 중단하므로, 핸들러를 직접 호출하는 검사는 `http.ErrAbortHandler`를 처리한다.

## 대용량 파일과 미리 읽기

블록은 삭제·LRU 관리 단위이고 세그먼트는 저장·읽기 단위다. 기본값은 각각 8 MiB / 2 MiB다. 재사용된 블록의 protected 예산은 디스크 한도의 80%이고 새 블록은 probation으로 들어간다. 순차 대용량 다운로드가 한 번 읽힌 기존 probation까지 보존하는 것은 아니며 protected도 영구 고정은 아니다. 재시작 시 우선순위는 probation으로 복구한다.

100 GiB 제한에서 200 GiB 파일을 읽어도 전체 파일을 먼저 저장하지 않는다. 제한 안에서 순차적으로 채우고 오래된 블록을 비운다. 기본 read-ahead window는 4세그먼트이며 인접 MISS를 한 Range GET으로 합친다. 현재 배치를 다 소비한 후 다음 배치를 채우는 방식이고 무제한 선다운로드나 지속적인 sliding-window prefetch는 아니다. 원본 속도·클라이언트 속도·지연에 따라 효과가 다르다.

디스크 한도는 저장된 세그먼트 바이트 기준이다. metadata, 디렉터리, 임시 쓰기, 열린 파일의 공간까지 포함하는 파일시스템 quota가 아니다. `CACHE_MAX_DISK_SIZE=0`은 원본과 동일하게 무제한이다. 요청 버퍼의 근사치는 window × segment이며 전체 프로세스 RSS의 상한이 아니다.

## HTTP 정책과 통합 시 보강

- 캐시에는 원본 헤더를 보관한다. browser/API의 private,no-store 및 MIME/CSP/attachment 정책은 응답 직전에 적용한다. 서버 캐시 HIT를 끄거나 browser 응답을 public으로 바꾸지 않는다.
- spa/direct는 원본 MIME·Content-Encoding·Content-Disposition과 TTL을 유지한다. 인증된 사이트 응답은 private으로 제한한다. Set-Cookie와 hop-by-hop 헤더는 전달하지 않는다.
- If-Match / If-None-Match / If-Modified-Since / If-Unmodified-Since / If-Range를 공통 처리한다. 304 재검증은 이전 헤더를 병합하고 `browser_ttl=0s`도 명시값으로 유지한다.
- HEAD는 원본 GET을 만들지 않는다. stale-if-error도 조건과 Range를 먼저 적용한다. 원본403/404는 stale 성공으로 바꾸지 않는다. ZIP은 stale을 허용하지 않는다.
- SPA/custom error 내부 읽기에 외부 파일의 Range·조건을 물려주지 않는다. 정확한 object API와 ZIP 오류는 사이트 HTML로 대체하지 않는다.
- `CACHE_ENABLED=false`와 off는 경로 TTL보다 우선한다. BYPASS 규칙은 metadata/negative 조회도 우회한다. 쿼리 ignore 여부도 TTL과 같은 첫 일치 규칙을 따른다.
- 응답 시작 후 실패는 오류 HTML/JSON을 덧붙이지 않고 HTTP 스트림을 중단한다. 미완료 세그먼트는 저장하지 않는다.
- 경로를 정규화해 다른 파일로 바꾸지 않고 dot segment·제어문자·역슬래시·중복 슬래시를 거부한다. `a..b` 같은 정상 이름은 허용한다.

권한 변경 감지는 metadata TTL만큼 늦어질 수 있다. 즉시 검사가 필요한 경로는 TTL0 또는 BYPASS로 설정한다. 공개 URL은 방문자가 S3 키 없이 읽도록 허용하는 기능이지 URL 자체의 접근통제 기능이 아니다.

## 설정과 운영

`SERVE_MODE` 기본은 browser다. 미설정 상태의 `SPA_MODE=true`는 spa alias이고 모순된 설정은 시작 오류다. `CACHE_MODE`는 internal(기본)과 off만 지원한다. off는 디스크를 열지 않는다. 외부 캐시 서버를 앞단에 두는 구성은 지원하지 않는다.

`INDEX_DOCUMENT` 미설정 시 browser는 빈 값, spa/direct는 index.html이다. 명시적인 빈 값은 인덱스 기능을 끈다. `HEALTH_PATH` 미설정은 /_mori/healthz, 빈 값은 HTTP probe를 끈다. `mori healthcheck`는 설정된 HTTP 경로를 사용하고 비활성 상태에서는 TCP 연결을 확인한다.

`LISTEN_ADDR`는 BROWSER_LISTEN_ADDR의 공통 alias다. 둘이 다르게 명시되면 오류다. AUTH_MODE basic/public을 명시하면 BROWSER_PUBLIC보다 우선한다. `SHUTDOWN_TIMEOUT`은 SIGTERM 이후 진행 중 전송을 기다리는 시간이며 양수여야 한다.

| 그룹 | 유지할 이름 |
|---|---|
| 저장소 선택/공개 범위 | STORAGE_BACKEND, STORAGE_BASE_PATH |
| browser/인증/listen/list | BROWSER_TITLE, BROWSER_LISTEN_ADDR, BROWSER_PORT(Compose용), BROWSER_USERNAME, BROWSER_PASSWORD, BROWSER_PUBLIC, BROWSER_LIST_TTL |
| 전달 | BROWSER_DOWNLOAD_MODE, BROWSER_PREVIEW_MODE, BROWSER_PRESIGN_TTL, BROWSER_PRESIGN_ENDPOINT |
| HTML | BROWSER_HTML_PREVIEW_ENABLED, BROWSER_HTML_PREVIEW_SCRIPTS, BROWSER_HTML_PREVIEW_EXTERNAL_RESOURCES |
| ZIP | BROWSER_ZIP_ENABLED, BROWSER_ZIP_MAX_FILES, BROWSER_ZIP_MAX_SIZE, BROWSER_ZIP_CONCURRENCY |
| S3 | S3_ENDPOINT, S3_REGION, S3_BUCKET, S3_ACCESS_KEY_ID, S3_SECRET_ACCESS_KEY, S3_SESSION_TOKEN, S3_FORCE_PATH_STYLE, S3_PREFIX |
| WebDAV | WEBDAV_URL, WEBDAV_USERNAME, WEBDAV_PASSWORD |
| FTP/FTPS | FTP_ADDR, FTP_USERNAME, FTP_PASSWORD, FTP_TLS, FTP_PATH |
| SFTP | SFTP_ADDR, SFTP_USERNAME, SFTP_PASSWORD, SFTP_KEY, SFTP_KEY_PASSPHRASE, SFTP_KEY_FILE, SFTP_HOST_KEY, SFTP_KNOWN_HOSTS, SFTP_INSECURE_HOST_KEY, SFTP_PATH |
| cache | CACHE_ENABLED, CACHE_DIR, CACHE_BLOCK_SIZE, CACHE_SEGMENT_SIZE, CACHE_MAX_DISK_SIZE, CACHE_DOWNLOAD_CONCURRENCY, CACHE_QUERY_MODE |
| TTL/정책 | CACHE_DEFAULT_TTL, CACHE_MIN_TTL, CACHE_MAX_TTL, CACHE_RESPECT_ORIGIN, CACHE_STALE_IF_ERROR, CACHE_NEGATIVE_TTL_404, CACHE_NEGATIVE_TTL_403, CACHE_RULES_JSON |
| 원본 fetch | ORIGIN_FETCH_MAX_SIZE, ORIGIN_MAX_CONCURRENT_REQUESTS |
| 사이트 | INDEX_DOCUMENT, SPA_MODE, SPA_INDEX, SPA_ALLOW_DOTTED_ROUTES |
| 오류 페이지 | ERROR_PAGE_404, ERROR_PAGES_JSON |
| 운영 | LISTEN_ADDR, HEALTH_PATH, ACCESS_LOG, SHUTDOWN_TIMEOUT |
| 모드/인증 | SERVE_MODE, CACHE_MODE, AUTH_MODE |

CACHE_RULES_JSON 필드는 prefix/suffix/ttl/browser_ttl/bypass/ignore_query다. SI/IEC·소수 바이트 단위와 cache duration의 d 단위를 유지한다. 음수·overflow·잘못된 bool·duration·블록 배수·잘못된 경로는 시작 시 검증한다. ERROR_PAGES_JSON은 동일 상태의 ERROR_PAGE_404보다 우선한다. ZIP의 기존 크기·기간 parser와 한도는 별개다.

Compose는 mori 하나와 /cache 영속 볼륨을 사용한다. 일반 실행의 CACHE_DIR 기본은 ./cache다. Docker와 release 이미지는 UID10001 및 읽기 전용 root filesystem에서도 /cache를 쓸 수 있게 구성했다. SIGTERM 시 진행 요청을 drain하고 SHUTDOWN_TIMEOUT 이후 남은 연결·fill을 중단한다.

저장소 종류·주소·사용자·루트·캐시 블록/세그먼트 크기로 namespace를 분리한다. 파일 내용은 별도 식별자별로 격리한다. 재시작 시 디스크를 재집계하고 persisted metadata를 첫 사용 전에 재검증한다. 같은 디스크 namespace를 여러 프로세스가 동시에 공유하지 않는다. 설정으로 namespace가 바뀐 이전 캐시 디렉터리는 자동 삭제하지 않는다.

환경변수 예제는 [../.env.example](../.env.example), 실행과 테스트 명령은 [README](../README.md)와 [검증 기록](VERIFICATION.md)을 참고한다.
