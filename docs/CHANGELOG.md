# Unreleased — 저장소 백엔드

- `STORAGE_BACKEND`로 S3(기본)·WebDAV·FTP/FTPS·SFTP 선택. 기존 S3 설정은 변경 없음.
- 목록·미리보기·개별 다운로드·Range·HEAD·조건부 요청·재귀 ZIP을 모든 백엔드에서 지원.
- `presigned` 전달과 `BROWSER_PROXY_URL` 캐시는 S3 전용. 다른 백엔드에서 지정하면 시작 오류.
- SFTP 호스트 키 검증 기본 적용(`SFTP_KNOWN_HOSTS`). 해제는 `SFTP_INSECURE_HOST_KEY=true`로 명시.
- `/api/config`에 `backend` 추가. 오류 메시지를 백엔드 이름에 맞춤. API 오류 코드는 유지.
- Go 1.26 필요. 새 의존성: `github.com/jlaffaye/ftp`, `github.com/pkg/sftp`, `golang.org/x/crypto`.
- 검증: 백엔드별 Go 단위 검사(인프로세스 WebDAV·FTP·SFTP 서버)와 Apache WebDAV·vsftpd·OpenSSH 실서버 검사(`tests/backends_e2e.sh`).

# 0.5.0 — 모바일 / 미리보기

- 모바일 파일명 아래 메타데이터, 정렬 메뉴, 넓은 터치 영역, safe-area 전체 화면 미리보기와 하단 선택 바.
- 이미지 / Media Chrome 오디오·영상 / PDF.js / 1 MiB 제한 안전한 텍스트 미리보기.
- 인증된 GET-only 미리보기 주소 API. proxy/presigned·선택 캐시·재귀 ZIP 유지.
- 빌드 시 고정 버전·integrity 검증 후 같은 서버에서 라이브러리 지연 로드.
- 실제 vendor 취득과 브라우저 HTTP 환경이 차단되어 라이브러리 통합 실행 미검증. 완료 범위는 VERIFICATION.md.

# 0.4.0

- 기본 Compose를 mori 단독 실행으로 변경. 외부 프록시 의존성과 디스크 볼륨 없음.
- 객체 디스크 캐시는 `compose.cache.yaml` 선택 연동으로 분리. 기존 프록시 직접 지정도 유지.
- `proxy` 전달 모드와 별도 캐시 서버의 차이, 단독 실행의 목록 캐시만 내장됨을 문서화.

- 검색 없음 / 화면 `delimiter=/` 한 단계 조회: 기존 0.3.0 구현을 확인하고 유지.
- 폴더 체크박스 추가. ZIP 선택 폴더에만 모든 하위 prefix/page를 포함.
- ZIP 상대 경로, 빈 폴더 마커, 중복 제거. 경로 탈출과 파일/폴더 충돌 거부.
- 재귀 파일의 크기/ETag는 새 S3 LIST에서 확인. 직접 파일 선택은 새 HEAD.
- 재귀 확장 후 개수/합계 용량 제한. ZIP 준비 단계에는 객체 본문 미전송.
- 파일 수 설정 가능 상한 1,000 → 10,000. 기본 200 유지.
- 대기 계획 전체 파일/폴더 메타데이터 20,000개 상한 추가. 기존 128개 계획 제한 유지.
- presign: 이전 GET/HEAD → GET Object 전용. HEAD는 서버 측 객체 경로로 처리.
- 기존 proxy/presigned 독립 모드, ZIP Store 스트리밍, 인증, 캐시 연결 유지.
- 새 테스트: 하위 1,005개 파일 ZIP 완주, 상위 비재귀 목록, 위험 경로/메타데이터/페이지 순환/한도/취소, GET 전용 presign.

운영 S3/Docker/부하 시험과 브라우저 HTTP E2E는 완료하지 않았습니다. 자세한 실행 결과는 VERIFICATION.md를 참조하세요.
