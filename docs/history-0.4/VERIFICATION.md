# 검증 기록 — 0.4.0

작성일: 2026-09-09. 이 기록은 포함된 0.4.0 소스에서 다시 실행한 결과입니다.

## 실행 완료

환경: Go 1.23.2, Node 22.16.0, Python 3.13, Playwright + Chromium.

| 검사 | 결과 및 범위 |
| --- | --- |
| `go test -race -count=1 -cover ./...` | 통과. statement coverage 86.8%. 테스트 캐시 없이 재실행. |
| `go vet ./...` | 통과. |
| `go build -trimpath` | 통과. 로컬 바이너리 빌드. |
| `node --check web/app.js` | 통과. |
| `python3 tests/http_smoke.py` | 통과. 실제 mori 바이너리 하나 + 모의 S3 HTTP 서버. **캐시 프록시 없음**. |
| `python3 tests/ui_smoke.py` | 통과. Chromium DOM/상호작용 검사. fetch/다운로드 링크 클릭은 테스트용 대체. |
| `python3 tests/compose_smoke.py` | 통과. PyYAML 기반 구조 검사. Docker Compose 엔진의 실제 해석/기동 검사가 아님. |
| `python3 tests/http_e2e.py` | 완료하지 못함. Chromium의 로컬 서버 탐색에서 `ERR_BLOCKED_BY_ADMINISTRATOR`. |

원문 출력은 같은 폴더의 `go-test-output.txt`, `http-test-output.txt`, `ui-test-output.txt`, `compose-test-output.txt`, `browser-http-e2e-output.txt`에 있습니다. Docker 빌드와 실제 S3 연결에 성공했다고 주장하지 않습니다.

## 단독 실행

별도 프로세스로 컴파일한 mori를 `BROWSER_PROXY_URL=''`로 실행했습니다. 외부 캐시 서버를 실행하지 않았습니다. `/api/config`의 direct 모드, 인증, 폴더 목록, 내부 목록 캐시 HIT, 개별 객체 본문, HEAD, 두 전달 설정의 네 가지 조합을 HTTP로 확인했습니다. proxy 모드에서 객체 본문 캐시를 우회하는 `X-Cache: BYPASS`도 확인했습니다.

개별 presigned URL은 HTTP 클라이언트로 따라가며 모의 S3가 botocore로 서명을 재계산한 후 본문을 반환합니다. 애플리케이션 로그인 자격 증명을 S3에 전달하지 않습니다. 파일 선택 ZIP과 폴더 재귀 ZIP은 Python zipfile로 해제해 경로·내용·Store·CRC를 확인했습니다.

기본 Compose에 mori 서비스 하나만 있고, depends_on/캐시 볼륨/원격 캐시 빌드가 없는지 정적으로 검사했습니다. `compose.cache.yaml`은 명시적으로 추가할 때만 캐시 의존성이 생기고 프록시 포트를 공개하지 않는지도 검사했습니다. 실제 `docker compose config`, Docker 이미지 빌드, Compose 기동은 실행하지 않았습니다.

## 화면 목록과 재귀 ZIP

모의 S3 계약 모델에서 하위 파일 10,000개가 상위 폴더 항목 하나로 묶이는지, 현재 단계의 파일 또는 폴더 1,001개는 다음 페이지를 통해 조회되는지 확인했습니다. 화면 조회의 `prefix`와 `delimiter=/`가 유지되고 객체 본문을 요청하지 않는 것을 검사했습니다.

새 테스트 `TestRecursiveZIPOver1000AndOneLevelListingIndependent`는 하위 파일 1,005개를 만들고, 상위 화면 목록은 직접 파일 2개 + 폴더 1개만 한 페이지로 읽는지 확인합니다. 같은 폴더를 ZIP으로 선택하면 2페이지를 모두 읽어 1,005개 파일의 완성 ZIP을 생성·해제합니다. 이 테스트에서는 파일 수 상한을 2,000으로 높였습니다. **제품 기본 상한은 200**이며 더 큰 폴더를 받으려면 설정을 높여야 합니다.

여러 페이지와 하위 경로, 한글/특수문자, 실제 0바이트 폴더 마커, 중복 선택/객체 제거, 준비 중 본문 미전송, 전체 파일 수·크기·폴더 항목 제한, 준비 취소, 잘못된 LIST 응답과 페이지 순환, 파일/폴더 경로 충돌 및 경로 탈출 거부를 검사했습니다. 대기 계획의 총 메타데이터 항목 수 제한도 검사했습니다.

## ZIP 스트림과 기존 기능 회귀 검사

Store(무압축), 파일 내용/CRC, 일회용 토큰 재사용 거부와 2분 만료, 대기 계획 128개 제한, 바쁜 상태 또는 Range 거부 시 토큰을 소모하지 않는 동작을 검사했습니다. 직접 파일의 HEAD와 재귀 LIST의 크기/ETag를 사용하고 클라이언트 크기를 신뢰하지 않습니다.

원본이 파일 전체를 보내기 전에 ZIP 바이트가 실제 로컬 HTTP 클라이언트에 도달하는지, 클라이언트 취소가 원본 요청에도 전달되는지 검사했습니다. 파일 변경/오류 시 응답 시작 전에는 오류로, 시작 후에는 완성 ZIP으로 마무리하지 않고 스트림을 중단하는지 검사했습니다. 완성 ZIP 임시 파일을 만들지 않습니다.

선택 캐시의 본문 경유와 메타데이터 원본 직접 조회는 Go 테스트의 모의 프록시로 검사했습니다. 실제 upstream s3-proxy 이미지/디스크 캐시를 실행한 것은 아닙니다. Range/HEAD/조건부 응답, 리다이렉트 차단, ENV 필수 설정, 읽기 전용 인증과 비밀정보 분리도 재검사했습니다.

## GET Object 전용 presign

AWS 공식 query 서명 예제와 botocore GET 픽스처를 비교했습니다. 세션 토큰, URI/쿼리 특수문자, 공개 엔드포인트 선적용, 브라우저 호스트/기본 포트 정규화를 검사했습니다. 이전 HEAD 픽스처는 HEAD presign을 거부하는 검사로 사용합니다.

`TestPresignOnlyGetObjectNotHeadOrListing`과 바이너리 HTTP 검사에서 HEAD는 Location을 발급하지 않고 서버 측 객체 경로로 처리하며, 목록도 presigned URL 없이 서버에서 S3로 요청하는 것을 확인했습니다. 다운로드/미리보기 설정은 독립적이며 GET Object에만 적용됩니다.

## UI

Chromium DOM 검사에서 검색/데모/패널 없음, 파일·폴더 체크박스, ZIP 준비 요청에 선택 폴더 키 포함, 전체 선택과 선택 상한, 다음 페이지/정렬 시 선택 유지, 폴더 이동 시 선택 초기화, 준비 실패/취소, 일반 다운로드 링크 시작(Blob으로 본문을 모으지 않음), 파일명 안전 렌더링, 빈 폴더/오류 상태를 확인했습니다. 1360×900과 390×844의 렌더링을 확인했으며 모바일 가로 넘침은 없었습니다.

fetch와 링크 클릭을 대체한 UI 검사이므로 브라우저의 실제 다운로드 저장까지 종단으로 검증한 것은 아닙니다. 서버 HTTP 테스트는 별도로 실제 소켓 연결을 사용했습니다.

## 미검증 / 제한

실제 AWS/MinIO/R2 연결, Docker 전체 빌드와 기동, upstream 객체 캐시, 공급자별 응답 헤더 재정의, 4GiB 이상 ZIP64·장시간/동시 사용자 부하, 브라우저 실제 HTTP 다운로드 종단 검사는 완료하지 않았습니다. 동적 ZIP의 Range 이어받기는 구현하지 않았습니다. 다중 S3 LIST/GET을 원자적 스냅샷으로 제공하지 않습니다.

서버 실행용 데모 모드나 샘플 객체는 없으며 테스트 데이터는 테스트 코드 안에만 존재합니다.
