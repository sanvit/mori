# 검증 범위

이 문서는 수행한 검사와 남은 운영 확인 범위를 구분합니다. 기능 명세는 [구조 문서](ARCHITECTURE.md)에 있습니다. 작은 테스트 파일과 대역을 사용한 검사를 실제 200 GiB 전송이나 운영 부하 검사로 간주하지 않습니다.

## 확인한 항목

| 검사 | 범위 |
|---|---|
| Go 테스트·race·vet | 저장소·서버·설정·미디어·목록 캐시, 경로 검증·조건부 요청·취소 |
| 캐시·프록시·세그먼트 단위 검사 | 보호 LRU·부분 HIT·연속 MISS 병합·공유 fill·read-ahead window·원본 동시 제한·취소·짧은/초과 응답·negative TTL·변경·재시작 |
| 제공 모드와 캐시 | browser/spa/direct × internal/off, 루트·정확한 object API·예약 경로·인증된 HIT·ZIP 캐시 공유·재시작 재검증 |
| HTTP 경계 조건 | 원본 Range 미지원 BYPASS, stale Range·403에서 stale 금지, HEAD의 GET 금지, browser_ttl0·첫 일치·query 정렬·원본 cookie 차단 |
| 파일 헤더·가상 ETag | 3모드 × internal/off × 공개/Basic 인증에서 UTF-8 파일명, GET/HEAD/304 Cache-Control 일치, browser no-store와 내부 HIT 동시 유지, weak 조건·ZIP 변경 감지·수정시각 없는 파일 BYPASS |
| WebDAV validator | HEAD 지원/미지원 × native strong/weak/누락/잘못된 ETag, PROPFIND 목록·Stat·HEAD 식별자 일치와 원본 캐시 헤더 유지 |
| HTTP/2 늦은 원본 검증 | FTP형 느린 최종 Stat 이전에 전체 응답을 완료하지 않음, 변경 감지 시 스트림 실패·캐시 무효화·재시도 |
| 실제 바이너리 HTTP 검사 | S3 모의 서버, 목록·HEAD·Range·오류, proxy/presigned 네 가지 조합, 독립 서명 검증 |
| 목록·ZIP UI | 정렬·선택·페이지·취소·오류, ZIP 비활성화와 모바일 선택 바 |
| 재귀 ZIP | 하위 1,005개 파일의 페이지 순회, Store/CRC·빈 폴더·상대 경로·한도·CSRF·일회용 token·변경 감지 |
| UI/DOM 미리보기 | 모바일 레이아웃·이미지 맞춤·텍스트 제한·포커스·Esc·뒤로 가기·요청 및 미디어/PDF 정리 |
| 실제 Chromium 뷰어 | PDF.js worker·연속 스크롤·proxy/presigned, Media Chrome 오디오 |
| 실제 WebKit 뷰어 | PDF.js worker·연속 스크롤·proxy/presigned, 비동기 스트림 호환 처리 |
| HTML 미리보기 | Chromium/WebKit, 스크립트·외부 리소스 네 조합, iframe·새 탭 정책 |
| PDF 표시 | inline/attachment, GET/HEAD/Range, 한글 파일명, 텍스트 추출 실패 후 페이지 유지 |
| SFTP ENV 인증·검증 | 인프로세스 SSH/SFTP 서버에서 inline/파일 개인 키·개행·암호화 키 인증, 서버 키 선택·불일치 거부, 잘못된 키·설정 충돌 거부; 실제 OpenSSH 컨테이너에서 비밀번호·키 파일 없이 두 키를 ENV로 전달해 목록·Range·ZIP·3모드 × internal/off 검사 |
| 실제 WebDAV·FTP·SFTP | Apache WebDAV·vsftpd·OpenSSH, 목록·없는 경로·한글·빈 파일·병렬 Range·ZIP·base path; 3모드 × internal/off에서 1 MiB 제한으로 3 MB 파일 전송 후 보호된 기존 캐시 HIT 확인 |
| 패키지 수집 | 합성 tarball 기반 allowlist 추출·무결성·변조/경로/잘못된 URL/설정 불일치 거부 |
| Compose | 단일 서비스·로컬 포트·내장 캐시 볼륨의 정적 YAML 검사 |
| 실제 Docker 이미지 | 이미지 빌드·non-root·읽기 전용 root·영속 볼륨에서 MISS/HIT·설정된 healthcheck·재시작 metadata 재검증과 본문 재사용 |
| 크로스 빌드 | Linux/macOS/Windows × amd64/arm64 컴파일 성공. 각 OS에서의 실제 실행 검사는 아님 |

DOM 검사와 실제 뷰어 검사는 별개입니다. DOM의 PDF API 대역으로 실제 PDF.js 실행을 검증했다고 주장하지 않습니다. Compose 정적 검사도 Docker 이미지 빌드·컨테이너 기동 검사와 다릅니다.

## 남은 확인 범위

- 실제 AWS/MinIO/R2 및 운영 저장소의 CORS·권한·호환 동작.
- FTPS 실서버 TLS 연결, 운영 OpenSSH의 개인 키 인증·키 교체, 다른 WebDAV 구현.
- iPhone/Android 실기기, WebKit 미디어 재생, WebM 재생과 다양한 실제 코덱.
- 실제 200 GiB 전송, 대용량 ZIP64·장시간 전송·부하·다중 운영 클라이언트에서의 RSS/디스크 측정.
- 운영 Compose의 실제 .env·프록시·권한·볼륨 배치. 테스트 이미지는 격리된 시험 환경에서 기동했습니다.

PDF Canvas 상한, 요청 취소, 구간 읽기는 자원 사용을 줄이지만 모든 파일·브라우저에서 일정한 메모리 사용량을 보장하지 않습니다. S3의 여러 LIST 페이지와 ZIP 전송도 원자적 저장소 스냅샷은 아닙니다.

## 재현

```sh
go test -race -count=1 -cover ./...
go vet ./...
node --check web/app.js
node --check web/preview.js
python3 -m unittest tests.unit.vendor_test
python3 -m tests.integration.http_smoke
python3 -m tests.ui.ui_smoke
python3 -m tests.ui.preview_dom
python3 -m tests.integration.compose_smoke
make assets
python3 tools/vendor.py --check
python3 -m tests.e2e.http_e2e
python3 -m tests.e2e.preview_e2e
MORI_TEST_BROWSER=webkit python3 -m tests.e2e.preview_e2e
sh tests/e2e/backends_e2e.sh
docker build -t mori:unified-test .
python3 -m tests.e2e.docker_smoke
```

실행에 필요한 테스트 의존성은 `tests/requirements.txt`에 있습니다. 서버 런타임에는 Python·Node·테스트 fixture가 필요하지 않습니다. 실제 저장소와 Docker 검사는 해당 환경을 준비한 뒤 수행해야 합니다.

화면 이미지와 모의 fixture는 UI 확인용이며 운영 저장소 연결 성공의 증거가 아닙니다.
