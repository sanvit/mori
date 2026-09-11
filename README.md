# mori — simple S3 file browser

h5ai처럼 경로와 파일 목록 위주로 사용하는 읽기 전용 파일 브라우저입니다. 저장소로 **S3, WebDAV, FTP/FTPS, SFTP**를 지원합니다. 서버는 Go로 작성했고 외부 의존성은 FTP·SFTP 클라이언트뿐입니다. 목록은 HTML/CSS/JavaScript로 작성했습니다. 미디어 컨트롤은 Media Chrome, PDF는 PDF.js를 사용합니다. h5ai 자체를 수정한 프로젝트는 아닙니다.

## 0.5.0 — 모바일 / 미리보기

파일 이름을 누르면 이미지·오디오·영상·PDF·텍스트 미리보기를 엽니다. 모바일에서는 크기/수정일을 파일명 아래에 표시하고, 전체 화면 미리보기와 하단 선택 다운로드 바를 제공합니다. Media Chrome 4.19.2 / PDF.js 6.3.289를 고정하고 **빌드 시 수집, 실행 시 같은 서버에서 제공**합니다. [미리보기 설정·형식·CORS·유지보수](docs/PREVIEW.md)를 참고하세요.

이 소스 ZIP에는 외부 뷰어 배포 파일을 미리 넣지 않았습니다. Docker 빌드가 가져오므로 **빌드 환경에는 인터넷 접근이 필요**합니다. 이번 환경에서는 외부 패키지 다운로드/브라우저 HTTP가 차단되어 실제 Media Chrome·PDF.js 통합 실행은 미검증입니다. 완료한 검사와 제한은 [검증 기록](docs/VERIFICATION.md)에 구분했습니다.

### 기존 기능 유지

**파일·폴더 선택 무압축 ZIP 다운로드**를 지원합니다. 선택한 폴더는 하위 파일을 재귀적으로 포함하며 ZIP 내부에 상대 경로를 보존합니다. 화면의 폴더 탐색은 재귀 조회가 아니라 한 단계 조회를 유지합니다. 개별 **다운로드와 미리보기는 각각 ENV에서 `proxy` 또는 `presigned`를 선택**합니다. 파일 경로·목록·정렬·새로고침은 유지하며 검색창, 데모 모드, 업로드/삭제, 즐겨찾기, 상세 패널은 없습니다.

## 설치

```sh
cp .env.example .env
# .env에 S3 연결 정보와 브라우저 인증을 설정합니다.
docker compose up --build -d
```

기본 접속 주소는 `http://localhost:8080`입니다. **기본 Compose는 mori 컨테이너 하나만 실행합니다. 외부 캐시 프록시, Redis, DB가 없어도 S3 목록·미리보기·개별 다운로드·재귀 ZIP이 동작합니다.** `.env`의 `BROWSER_PROXY_URL`은 비워 두세요. `proxy` 전달 모드는 mori의 중계를 뜻하며 별도 프록시가 필수라는 뜻이 아닙니다. S3 저장소와 네트워크 접근은 필요합니다.

단독 실행에도 `BROWSER_LIST_TTL`의 목록 메모리 캐시는 있습니다. **파일 본문의 디스크/세그먼트 캐시는 내장하지 않았습니다.** `CACHE_ENABLED=true`만으로 내장 캐시가 생기지는 않습니다.

### 선택 사항: 객체 디스크 캐시

`s3-proxy` 캐시를 함께 실행할 때만 추가 Compose 파일을 지정합니다.

```sh
docker compose -f compose.yaml -f compose.cache.yaml up --build -d
```

이때만 mori의 `BROWSER_PROXY_URL`을 `http://cache-proxy:8080`으로 설정하고 캐시 상태를 기다립니다. 캐시 프록시 소스는 고정 커밋을 원격 빌드하므로 빌드에 인터넷이 필요합니다. 출처와 고정 커밋은 `NOTICE.md`에 있습니다. 캐시 프록시 코드를 이 저장소에 복사하지 않았습니다. 이미 실행 중인 호환 프록시가 있다면 추가 Compose 없이 `BROWSER_PROXY_URL`만 그 주소로 설정해도 됩니다.

이전의 캐시 포함 Compose에서 단독 실행으로 바꿀 때에는 `.env`의 `BROWSER_PROXY_URL`을 비우고 다음처럼 불필요한 컨테이너를 제거합니다. 기존 캐시 볼륨은 삭제하지 않습니다.

```sh
docker compose up --build -d --remove-orphans
```

### Docker 없이 실행

직접 실행도 가능합니다.

```sh
make assets     # Python 3, 빌드용 정적 파일 수집
make run        # 또는 make build -> bin/mori
```

`.env`를 읽으며 프로세스 환경변수가 우선합니다. Go 1.26 이상이 필요합니다. Docker 전체 빌드/기동은 이번 환경에서 검증하지 않았습니다. 테스트용 의존성은 `tests/requirements.txt`에 있고 서버 실행에는 필요하지 않습니다.

최소 설정:

```dotenv
S3_ENDPOINT=https://s3.ap-northeast-2.amazonaws.com
S3_REGION=ap-northeast-2
S3_BUCKET=your-bucket
S3_ACCESS_KEY_ID=your-access-key
S3_SECRET_ACCESS_KEY=your-secret-key
S3_SESSION_TOKEN=
S3_FORCE_PATH_STYLE=true
S3_PREFIX=
BROWSER_USERNAME=admin
BROWSER_PASSWORD=replace-with-a-long-unique-password
BROWSER_PUBLIC=false
```

키/비밀키를 모두 비우면 익명 요청을 사용하지만 **presigned 모드는 자격 증명이 필수**입니다. 기본 설정은 인증 없이는 시작하지 않습니다. 의도적인 공개 파일 서버는 사용자명/비밀번호를 모두 비우고 `BROWSER_PUBLIC=true`를 명시해야 합니다. 이 값은 S3 버킷 정책을 바꾸지 않고 mori의 접근을 공개합니다.

기존 `.env`의 `BROWSER_DEMO`는 삭제하세요. 실행 코드에서 더 이상 읽지 않으며 샘플 목록으로 대체하지 않습니다.

## 저장소 백엔드

`STORAGE_BACKEND`로 한 가지 저장소를 고릅니다. 기본값은 `s3`이며 기존 설정은 그대로 동작합니다. 각 백엔드의 ENV는 `.env.example`에 있습니다.

| 기능 | S3 | WebDAV | FTP/FTPS | SFTP |
|---|---|---|---|---|
| 목록·미리보기·다운로드 (`proxy`) | O | O | O | O |
| Range·HEAD·조건부 요청 | O | O | O | O |
| 재귀 ZIP | O | O | O | O |
| `presigned` 전달 | O | X | X | X |
| `BROWSER_PROXY_URL` 캐시 | O | X | X | X |

**S3가 아닌 백엔드는 항상 mori를 거쳐 전달합니다.** `BROWSER_DOWNLOAD_MODE`나 `BROWSER_PREVIEW_MODE`를 `presigned`로 두거나 `BROWSER_PROXY_URL`을 지정하면 시작 단계에서 오류로 멈춥니다. `/api/config`는 `backend` 값과 실제 적용된 `proxy` 모드를 알려 줍니다.

```dotenv
# WebDAV
STORAGE_BACKEND=webdav
WEBDAV_URL=https://dav.example.com/files/
WEBDAV_USERNAME=reader
WEBDAV_PASSWORD=...

# FTP (FTP_TLS=true는 명시적 FTPS)
STORAGE_BACKEND=ftp
FTP_ADDR=ftp.example.com
FTP_USERNAME=reader
FTP_PASSWORD=...
FTP_PATH=/pub

# SFTP
STORAGE_BACKEND=sftp
SFTP_ADDR=sftp.example.com
SFTP_USERNAME=reader
SFTP_KEY_FILE=/run/secrets/id_ed25519
SFTP_KNOWN_HOSTS=/run/secrets/known_hosts
SFTP_PATH=/srv/files
```

백엔드별 동작과 제한:

- **파일 버전 확인.** S3는 ETag를 씁니다. WebDAV는 서버의 강한 ETag가 있으면 쓰고, 없으면 크기와 수정 시각으로 만듭니다. FTP·SFTP는 크기와 수정 시각으로 만듭니다. ZIP 도중 파일이 바뀌면 크기·시각 비교와 전송 길이 검사로 중단합니다. 다만 FTP `LIST`는 보통 분 단위 시각만 주므로, 같은 분 안에 크기를 유지한 채 바뀐 파일은 감지하지 못합니다.
- **WebDAV**는 `PROPFIND`(Depth 0/1)와 Range GET을 씁니다. Basic 인증만 지원하며 다른 호스트를 가리키는 href는 무시합니다.
- **FTP**는 로그인된 연결을 최대 4개까지 재사용합니다. 목록 조회는 서버가 지원하면 MLSD, 아니면 LIST를 씁니다. 심볼릭 링크는 목록에서 뺍니다. 없는 폴더를 빈 목록으로 답하는 서버(vsftpd 등)에서도 404를 돌려주도록 상위 폴더를 확인합니다.
- **SFTP**는 SSH 연결 하나를 모든 요청이 공유하고, 끊기면 다음 요청에서 다시 연결합니다. **호스트 키 검증이 기본**이며 `SFTP_KNOWN_HOSTS`가 필요합니다. 파일을 가리키는 심볼릭 링크는 따라가고, 폴더 링크는 순환을 막기 위해 목록에서 뺍니다.
- **목록 페이지.** WebDAV·FTP·SFTP는 폴더 하나를 한 번에 조회합니다. 항목이 매우 많은 폴더는 S3보다 느릴 수 있습니다. 재귀 ZIP은 하위 폴더 10,000개에서 멈춥니다.
- 비밀번호와 키는 로그나 API 응답에 넣지 않습니다. 접근 거부 시 서버 로그에는 백엔드 이름과 오류 코드만 남깁니다.

## 다운로드 / 미리보기 전달 방식

아래 `presigned` 설명은 S3 백엔드에만 해당합니다.

예: 개별 다운로드는 S3 직접 링크, 미리보기는 mori 경유. 캐시 서버 없이도 가능합니다.

```dotenv
BROWSER_DOWNLOAD_MODE=presigned
BROWSER_PREVIEW_MODE=proxy
BROWSER_PRESIGN_TTL=15m
```

두 모드의 기본값은 모두 `proxy`입니다. 두 값을 독립적으로 조합할 수 있으며 화면에는 별도의 설정 패널을 추가하지 않았습니다. 미리보기는 파일 이름 클릭으로 목록 위 대화상자에서 열고, 다운로드는 우측 아이콘을 사용합니다. 미리보기 안의 원본 열기/수정 키 클릭은 기존 새 탭 경로를 유지합니다. 지원 형식과 브라우저 코덱 제한은 `docs/PREVIEW.md`를 참고하세요. HTML/SVG/텍스트/JSON은 안전한 원문으로 표시하며, 알 수 없는 형식은 다운로드로 처리합니다.

### proxy

```text
S3 → [선택: s3-proxy 캐시] → mori → 사용자
```

mori가 파일을 스트리밍합니다. `BROWSER_PROXY_URL`을 지정하면 객체 본문을 기존 캐시 프록시에서 받고, 비우면 S3에서 직접 받아 전달합니다. **URL을 비워도 사용자가 S3에 직접 접속하는 것이 아니라 mori를 거칩니다.** 기본 Compose는 외부 프록시를 강제하지 않으며, 선택 파일 `compose.cache.yaml`을 추가할 때만 `http://cache-proxy:8080`으로 설정합니다.

개별 파일의 GET/HEAD, Range와 조건부 요청을 전달합니다. 파일 전체를 RAM이나 완성 임시 파일로 모으지 않습니다. 프록시 사용 시 실제 캐시 디스크/세그먼트 버퍼는 별도 프록시가 사용합니다.

### presigned

**PDF·텍스트 직접 미리보기에는 S3 CORS 설정이 필요합니다.** `docs/s3-cors.example.json`의 AllowedOrigins를 실제 화면의 출처로 바꿔 적용하세요. [상세 설명](docs/PREVIEW.md#presigned-pdf텍스트-s3-cors)을 참고하세요.

```text
사용자 → mori에서 인증·키 범위 확인 → HTTP 307 → S3 → 사용자
```

개별 다운로드/원본 열기에서는 mori가 SigV4 서명 URL로 리다이렉트하고, 내장 미리보기에서는 `/api/preview`가 GET 서명 URL을 JSON으로 발급합니다. 어느 쪽도 mori가 객체 본문을 받지 않습니다. 이 경로는 **mori와 객체 캐시를 우회**합니다. 링크를 누를 때 새 URL이 발급되며, URL 전체를 사전에 목록에 넣거나 저장하지 않습니다. GET에는 서명된 Content-Type/Content-Disposition 응답 재정의를 붙여 다운로드/미리보기 목적을 유지합니다. **GetObject(GET)만 presign합니다.** HEAD 요청은 설정과 관계없이 서버 측 객체 경로로 처리합니다. ListObjectsV2, 버킷 조회, ZIP, 업로드/삭제를 presign하는 경로는 없습니다. 목록은 서버에서 S3로 요청합니다. GET presigned URL에 HEAD를 보내는 방식도 사용하지 않습니다.

`BROWSER_PRESIGN_TTL`은 기본 15분이며 1초 이상 7일 이하의 정수 초입니다. STS 세션 토큰을 사용하면 세션이 먼저 끝날 경우 URL도 그보다 오래 유효하지 않습니다. IAM 역할 자격 증명의 자동 탐색/갱신은 구현하지 않았으며 ENV의 키와 선택적 세션 토큰을 사용합니다.

**서명 URL 자체는 유효기간 동안 접근 권한을 가진 링크**입니다. 비밀 접근 키는 URL에 들어가지 않지만 접근 키 ID, 서명, 사용하는 경우 세션 토큰은 서명 프로토콜에 따라 URL에 포함됩니다. 링크를 공유하거나 쿼리 문자열을 로그에 남기지 마세요. S3 접근 실패/만료 시 자동으로 proxy로 우회하지 않습니다.

S3 호환 공급자의 SigV4와 GetObject 응답 헤더 재정의 지원은 실제 서비스에서 확인해야 합니다. S3 웹사이트 호스팅 URL이 아니라 **S3 API 엔드포인트**가 필요합니다. 일반 페이지 탐색/다운로드 링크를 사용하며 파일 본문을 JavaScript fetch/Blob으로 읽지 않습니다.

### 내부 주소와 공개 주소가 다른 S3 / MinIO

```dotenv
S3_ENDPOINT=http://minio:9000
BROWSER_PRESIGN_ENDPOINT=https://objects.example.com
```

선택 설정 `BROWSER_PRESIGN_ENDPOINT`가 비어 있으면 `S3_ENDPOINT`를 사용합니다. 공개 주소는 사용자의 브라우저에서 도달 가능해야 하며 같은 버킷·리전·path-style 설정에 대응해야 합니다. **공개 주소를 먼저 적용한 다음 서명**합니다. 서명 뒤 호스트를 바꾸지 않습니다. IDN 호스트는 ASCII punycode 형식으로 설정하세요. 운영 환경에서는 HTTPS를 사용하세요.

## 파일·폴더 선택 ZIP

파일 또는 폴더를 체크하면 상단에 `ZIP 다운로드 (N)` 버튼이 나타납니다. N은 선택한 최상위 항목 수이며 폴더 안의 실제 파일 수는 준비 요청 후 서버에서 확인합니다. 전체 선택은 **현재 폴더에서 이미 불러온 파일·폴더 항목**을 선택합니다. 아직 읽지 않은 현재 폴더의 다음 페이지는 선택하지 않지만, **선택한 폴더 안의 하위 파일은 UI에서 열어보지 않았어도 모든 페이지를 순회해 포함**합니다. 상위 폴더 이동용 `..`는 선택하지 않습니다. 더 불러오기와 정렬은 선택을 유지하고, 폴더 이동이나 새로고침은 선택을 초기화합니다. 파일 개수 제한보다 목록이 많으면 전체 선택을 거부하고 개별 선택하도록 안내합니다.

```dotenv
BROWSER_ZIP_MAX_FILES=200
BROWSER_ZIP_MAX_SIZE=20GiB
BROWSER_ZIP_CONCURRENCY=2
```

위 값은 기본값입니다. 파일 수는 1–10,000, 동시 요청은 1–16으로 설정 가능합니다. 크기는 `20GiB`, `500MB`, `1024`처럼 정수와 단위를 사용하며 상한은 1PiB입니다. 파일 수 제한은 **재귀 확장한 전체 파일 합계**에 적용합니다. 200개가 기본이므로 더 큰 폴더는 ENV 상한을 올려야 합니다. 상한을 넘으면 일부만 담지 않고 준비 단계에서 요청을 거부합니다. 0바이트 폴더 마커 항목도 같은 값의 별도 개수 상한을 적용해 빈 폴더만으로 메모리가 늘어나지 않도록 합니다. 용량 제한은 ZIP 헤더/목차를 제외한 **하위 파일을 포함한 원본 크기 합계**입니다. 큰 상한이 해당 서버 성능을 보증한다는 뜻은 아닙니다.

ZIP은 `archive/zip`의 **Store(무압축)** 방식입니다. 완성 ZIP을 서버 디스크나 RAM에 만들지 않고 객체를 한 개씩 읽어 응답에 씁니다. 압축 CPU 연산은 없지만 CRC32 계산, 데이터 복사, 네트워크 전송, 사용하는 경우 객체 캐시 I/O는 남습니다. 버퍼와 ZIP 목차 메타데이터를 사용하므로 메모리가 0인 것은 아닙니다.

**ZIP은 두 개별 파일 모드 설정과 관계없이 항상 서버 경유**입니다. 본문은 `BROWSER_PROXY_URL`이 비어 있으면 S3에서 직접 읽고, 설정되어 있으면 해당 캐시 경로를 사용합니다. 별도 캐시가 없어도 ZIP은 동작합니다. 선택 파일을 모아 새 ZIP을 S3에 업로드하거나 그 ZIP의 presigned URL을 만드는 구현은 아닙니다.

### 준비 / 전송

1. UI는 `POST /api/archive`에 `{ "prefix": "docs/", "keys": ["docs/a.txt", "docs/photos/"] }`만 보냅니다. `/`로 끝나는 선택 키는 폴더입니다. 같은 origin의 JSON 요청과 `X-Mori-Request: 1` 헤더를 요구합니다.
2. 서버가 현재 폴더의 직접 자식만 선택했는지와 키 범위/중복을 검사합니다. 직접 선택한 파일은 원본 S3 HEAD로 크기·ETag를 확인합니다. 선택 폴더는 **그 prefix만 지정하고 delimiter 없이 ListObjectsV2의 모든 페이지를 순회**합니다. 재귀 파일의 크기·ETag는 이 새 LIST 응답을 사용하므로 파일마다 추가 HEAD를 보내지 않습니다. UI가 보낸 크기와 기존 목록 캐시는 신뢰하지 않습니다. 준비 전체는 60초 제한이며 객체 본문은 받지 않습니다. 타임아웃·원본 오류·한도 초과이면 일회용 URL을 발급하지 않습니다.
3. 2분 동안 유효한 일회용 다운로드 URL을 반환합니다. 브라우저는 해당 URL을 일반 다운로드 링크로 열어 ZIP을 받습니다. JavaScript가 ZIP 전체를 Blob으로 모으지 않습니다.

준비 요청과 진행 중 ZIP 스트림은 같은 동시성 제한을 공유합니다. 대기 다운로드 메타데이터는 인스턴스당 최대 128개 계획 및 전체 20,000개 파일·폴더 항목으로 제한하며 만료 항목은 요청 시 정리합니다. 토큰은 재사용할 수 없고 서버 재시작 시 사라집니다. 여러 mori 인스턴스로 분산하면 준비와 다운로드가 같은 인스턴스로 가도록 세션 고정이 필요합니다.

ZIP 내부 경로는 현재 화면 폴더 기준입니다. `docs/`에서 `a.txt`와 `photos/`를 선택하면 다음처럼 보존합니다.

```text
files.zip (현재 폴더가 docs/이면 docs.zip)
├── a.txt
└── photos/
    ├── image.jpg
    └── nested/another.jpg
```

중복 선택과 페이지 간 동일 객체는 한 번만 포함합니다. 실제 S3 0바이트 폴더 마커는 빈 디렉터리로 보존하며, 일반 파일의 부모 디렉터리는 ZIP의 상대 경로로 표현합니다. 선택 prefix에 객체나 마커가 하나도 없으면 사라진 폴더로 보고 404를 반환합니다. 버킷 전체 스캔으로 폴더를 찾지 않습니다.

절대 경로·`..`·역슬래시·콜론 등 위험한 ZIP 경로는 거부합니다. S3에 `a`와 `a/b`처럼 같은 경로가 파일·디렉터리를 동시에 의미하면 ZIP 추출 시 덮어쓰기가 생기지 않도록 요청을 거부합니다. 내용이 있는 `/` 끝 객체를 빈 폴더로 바꾸어 버리지 않고 거부합니다. 대소문자 충돌과 기타 운영체제별 이름 제약은 압축 해제 환경에 따릅니다.

### 변경 / 중단 / 이어받기

ZIP 전송은 HEAD 또는 재귀 LIST에서 확인한 ETag로 If-Match를 보내고 GET 응답 ETag와 길이도 확인합니다. 파일 변경이나 원본 실패가 응답 시작 전이면 오류를 반환하고, 시작 후이면 연결을 중단합니다. 일부 파일만 든 ZIP을 성공한 것처럼 완성하지 않습니다. 캐시 프록시가 오래된 버전을 반환하는 경우에도 ETag가 다르면 실패합니다.

S3의 여러 페이지 목록 조회와 파일 다운로드를 하나의 원자적 스냅샷으로 만들지는 않습니다. 준비 도중 추가된 파일이 모두 포함된다고 보장하지 않으며, 준비한 파일이 이후 변경/삭제되면 오류로 처리합니다. 클라이언트 연결 종료는 재귀 LIST와 진행 중 S3/프록시 요청에 전파합니다. 동적 ZIP에는 완성본과 고정 Content-Length가 없으며 **Range 이어받기는 지원하지 않습니다**. 중단/실패하면 다시 선택해 새 ZIP을 요청해야 합니다. 전체 백분율 표시도 브라우저에 따라 제한됩니다.

외부 리버스 프록시는 원래 Host를 보존하고 응답 버퍼링/다운로드 타임아웃을 검토하세요. ZIP 응답에는 `X-Accel-Buffering: no`를 넣지만 외부 프록시 설정까지 강제로 바꾸지는 못합니다.

## 폴더 목록 / 검색

목록은 기존처럼 `ListObjectsV2(prefix=현재 경로, delimiter=/, max-keys=1000)`으로 한 단계씩 읽습니다. **하위 폴더 안의 파일 10,000개는 상위 목록에서 폴더 한 항목**입니다. 하위 파일을 모두 읽어서 숨기는 구조가 아니고 파일 본문도 받지 않습니다.

현재 단계의 파일과 바로 아래 폴더 항목들이 한 응답에 다 들어오지 않을 때만 continuation token으로 다음 페이지를 읽습니다. 다음 요청에도 prefix/delimiter를 유지합니다. ‘더 불러오기’ 전에는 다음 페이지를 자동 순회하지 않으며 정렬도 불러온 항목에만 적용합니다. 다음 페이지가 있으면 하단에 표시합니다. 검색창과 파일명 필터는 제거되어 있습니다. 별도 전체 키 검색 인덱스도 만들지 않습니다. 이 동작과 `delimiter=/` 한 단계 조회는 0.3.0에도 이미 들어가 있었습니다.

`BROWSER_LIST_TTL=30s`는 폴더 목록 메모리 캐시이며 `0s`로 끌 수 있습니다. 최대 512페이지를 보관합니다. 새로고침은 현재 폴더 첫 페이지 목록 캐시를 우회하며 객체 캐시를 삭제하지 않습니다.

## 운영

Compose는 기본적으로 브라우저 포트를 127.0.0.1에만 바인딩합니다. 외부 제공 시 HTTPS와 인증을 유지하세요. 캐시 프록시에는 브라우저 인증이 없으므로 내부 포트를 외부에 공개하지 마세요. S3 비공개 버킷이어도 `BROWSER_PUBLIC=true`면 mori가 그 범위를 사용자에게 제공할 수 있습니다.

선택 캐시 서비스를 사용하는 경우에만 두 서비스의 버킷과 prefix를 일치시켜야 합니다. 캐시 ENV 이름과 기본값은 기존 `s3-proxy` 방식을 유지했습니다. 디렉터리 인덱스, SPA fallback, 커스텀 오류 페이지는 없는 객체가 정상 HTML로 대체되지 않도록 꺼 두었습니다.

새 소스를 적용한 뒤 `docker compose up --build -d`로 재빌드하세요. ENV만 바꾸었다면 `docker compose up -d --force-recreate`로 반영합니다. 선택 캐시를 쓰는 경우 이 명령들에도 `-f compose.yaml -f compose.cache.yaml`을 동일하게 추가하세요. 직접 실행 중이면 프로세스를 재시작하세요.

## 코드 구조

```
cmd/mori/          진입점. .env 로드, 설정 검증, HTTP 서버 기동
internal/config/   환경변수 파싱·검증, 객체 키 검증
internal/backend/  저장소 인터페이스(List·Walk·Stat·Open)와 공통 타입
  webdav/ ftp/ sftp/  S3 외 백엔드 구현
internal/media/    확장자 기반 MIME·미리보기 종류·안전한 Content-Type/Disposition
internal/s3/       SigV4 서명, 한 단계 목록, 객체 GET/HEAD, presign, ZIP용 재귀 순회
internal/cache/    목록용 TTL 캐시 (동시 요청 합치기)
internal/server/   인증·보안 헤더, JSON API, 객체 전달(S3 통과/범용), ZIP 준비/스트리밍, 정적 파일
web/               브라우저 UI. embed.go가 바이너리에 임베드 (vendor/는 빌드 시 생성)
tools/vendor.py    Media Chrome / PDF.js 수집 스크립트
tests/             Python 통합·UI 검사와 서명 fixture
docs/              설정·검증 문서
```

`internal/` 아래는 외부에서 import할 수 없는 구현 패키지입니다. `server`는 `backend.Backend` 인터페이스로 모든 저장소를 다룹니다. S3일 때만 presign과 캐시 프록시 통과 경로를 추가로 씁니다. 새 백엔드는 `internal/backend/` 아래에 네 메서드를 구현하고 `cmd/mori`와 `internal/config`에 연결하면 됩니다.

## 테스트

```sh
go test -race -cover ./...
go vet ./...
node --check web/app.js
node --check web/preview.js
python3 -m unittest discover -s tests -p "vendor_test.py" -v

# 테스트 전용 의존성. Go 서버의 실행 의존성이 아닙니다.
python3 tests/preview_dom.py    # 테스트용 응답/PDF 대역으로 UI 수명주기·모바일 검사
python3 tests/ui_smoke.py       # Playwright + Chromium, DOM/모의 fetch 검사
python3 tests/http_smoke.py     # botocore, 실제 바이너리 + 모의 S3 HTTP 검사
python3 tests/compose_smoke.py  # PyYAML 구조 검사; Docker 실행 검사가 아님
tests/backends_e2e.sh           # Docker로 Apache WebDAV·vsftpd·OpenSSH를 띄워 실제 바이너리 검사
python3 tests/http_e2e.py       # 실제 브라우저 HTTP 목록/ZIP/텍스트 검사
make assets
python3 tests/preview_e2e.py    # 실제 Media Chrome/PDF.js + HTTP, 환경상 이번 실행은 미완료
```

`tests/http_e2e.py`는 이번 환경의 브라우저 로컬 URL 정책으로 완료하지 못했습니다. 대신 UI 검사와 바이너리 HTTP 통합 검사를 분리해 실행했습니다. 새 재귀 ZIP은 하위 1,005개 파일의 2페이지 조회·완성 ZIP 검사를 포함합니다. 실제 AWS/MinIO/R2와 Docker 전체 기동, 대용량 ZIP64/부하 시험은 미검증입니다. 완료한 검사와 범위는 `docs/VERIFICATION.md`를 참고하세요. 테스트용 데이터는 테스트 코드에서만 생성하며 실행 바이너리에 샘플 파일을 넣지 않습니다.

## 공식 문서 / 출처

- Media Chrome 4.19.2: https://github.com/muxinc/media-chrome/releases/tag/v4.19.2
- PDF.js 6.3.289: https://github.com/mozilla/pdf.js/releases/tag/v6.3.289
- Media Chrome 사용법: https://www.media-chrome.org/docs/en/get-started
- PDF.js API: https://mozilla.github.io/pdf.js/api/draft/module-pdfjsLib.html
- S3 CORS: https://docs.aws.amazon.com/AmazonS3/latest/userguide/cors.html

- S3 ListObjectsV2: https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListObjectsV2.html
- S3 GetObject / response header overrides: https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObject.html
- SigV4 query authentication: https://docs.aws.amazon.com/AmazonS3/latest/developerguide/sigv4-query-string-auth.html
- Presigned URL lifetime/credentials: https://docs.aws.amazon.com/AmazonS3/latest/userguide/using-presigned-url.html
- Go ZIP Store: https://pkg.go.dev/archive/zip
- Go ZIP writer: https://go.dev/src/archive/zip/writer.go
- Compose 파일 병합: https://docs.docker.com/compose/how-tos/multiple-compose-files/merge/
- 캐시 프록시 출처와 커밋: `NOTICE.md`
