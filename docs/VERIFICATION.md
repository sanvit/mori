# 검증 범위

이 문서는 수행한 검사와 남은 확인 범위를 정리합니다. 저장된 실행 기록을 근거로 하며, 통합 설계의 구현 완료를 의미하지 않습니다.

## 확인한 항목

| 검사 | 범위 |
|---|---|
| Go 테스트·race·vet | 저장소·서버·설정·미디어·목록 캐시, 경로 검증·조건부 요청·취소 |
| 실제 바이너리 HTTP 검사 | S3 모의 서버, 목록·HEAD·Range·오류, proxy/presigned 네 가지 조합, 독립 서명 검증 |
| 목록·ZIP UI | 정렬·선택·페이지·취소·오류, ZIP 비활성화와 모바일 선택 바 |
| 재귀 ZIP | 하위 1,005개 파일의 페이지 순회, Store/CRC·빈 폴더·상대 경로·한도·CSRF·일회용 token·변경 감지 |
| UI/DOM 미리보기 | 모바일 레이아웃·이미지 맞춤·텍스트 제한·포커스·Esc·뒤로 가기·요청 및 미디어/PDF 정리 |
| 실제 Chromium 뷰어 | PDF.js worker·연속 스크롤·proxy/presigned, Media Chrome 오디오 |
| 실제 WebKit 뷰어 | PDF.js worker·연속 스크롤·proxy/presigned, 비동기 스트림 호환 처리 |
| HTML 미리보기 | Chromium/WebKit, 스크립트·외부 리소스 네 조합, iframe·새 탭 정책 |
| PDF 표시 | inline/attachment, GET/HEAD/Range, 한글 파일명, 텍스트 추출 실패 후 페이지 유지 |
| WebDAV·FTP·SFTP | 인프로세스 서버와 Apache WebDAV·vsftpd·OpenSSH, 목록·없는 경로·한글·빈 파일·병렬 Range·ZIP·base path |
| 패키지 수집 | 합성 tarball 기반 allowlist 추출·무결성·변조/경로/잘못된 URL/설정 불일치 거부 |
| Compose | 단일 서비스·로컬 포트·외부 프록시 선택 설정의 정적 YAML 검사 |

DOM 검사와 실제 뷰어 검사는 별개입니다. DOM의 PDF API 대역으로 실제 PDF.js 실행을 검증했다고 주장하지 않습니다. Compose 정적 검사도 Docker 이미지 빌드·컨테이너 기동 검사와 다릅니다.

## 남은 확인 범위

- 실제 AWS/MinIO/R2 및 운영 저장소의 CORS·권한·호환 동작.
- FTPS 실서버 TLS 연결, SFTP 개인 키 인증 실서버 연결, 다른 WebDAV 구현.
- iPhone/Android 실기기, WebKit 미디어 재생, WebM 재생과 다양한 실제 코덱.
- 전체 브라우저 HTTP 시나리오, Docker 전체 빌드/기동, 대용량 ZIP64·장시간 전송·부하.
- 내장 객체 캐시와 browser/spa/direct 모드: 설계 단계이며 아직 구현하지 않았습니다.

PDF Canvas 상한, 요청 취소, 구간 읽기는 자원 사용을 줄이지만 모든 파일·브라우저에서 일정한 메모리 사용량을 보장하지 않습니다. S3의 여러 LIST 페이지와 ZIP 전송도 원자적 저장소 스냅샷은 아닙니다.

## 재현

```sh
go test -race -count=1 -cover ./...
go vet ./...
node --check web/app.js
node --check web/preview.js
python3 -m unittest discover -s tests -p "vendor_test.py" -v
python3 tests/http_smoke.py
python3 tests/ui_smoke.py
python3 tests/preview_dom.py
python3 tests/compose_smoke.py
make assets
python3 tools/vendor.py --check
python3 tests/http_e2e.py
python3 tests/preview_e2e.py
MORI_TEST_BROWSER=webkit python3 tests/preview_e2e.py
tests/backends_e2e.sh
```

실행에 필요한 테스트 의존성은 `tests/requirements.txt`에 있습니다. 서버 런타임에는 Python·Node·테스트 fixture가 필요하지 않습니다. 실제 저장소와 Docker 검사는 해당 환경을 준비한 뒤 수행해야 합니다.

화면 이미지와 모의 fixture는 UI 확인용이며 운영 저장소 연결 성공의 증거가 아닙니다.
