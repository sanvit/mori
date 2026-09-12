# 테스트 실행

Go의 패키지 내부 테스트는 테스트 대상 코드 옆의 `*_test.go`에 둡니다. 비공개 구현을 검사하기 위해 운영 API를 추가로 공개하지 않습니다. 외부 프로세스·브라우저 검사와 공유 fixture는 이 폴더에 모읍니다.

| 위치 | 역할 |
|---|---|
| `integration/` | 실제 바이너리 HTTP 및 Compose 설정 검사 |
| `e2e/` | 브라우저 HTTP, 실제 뷰어·저장소·Docker 검사 |
| `ui/` | 모의 응답을 사용하는 UI/DOM 검사 |
| `unit/` | Python 도구 단위 테스트 |
| `fixtures/` | 공통 S3 HTTP 대역, 서명 fixture와 생성기 |

저장소 루트에서 실행합니다. Go, Node, Python이 필요하고 브라우저·컨테이너 검사에는 각각 Playwright 브라우저와 Docker가 필요합니다. 저장소 E2E는 `ssh-keygen`으로 일회용 SFTP 인증 키를 생성합니다.

```sh
python3 -m pip install -r tests/requirements.txt
python3 -m playwright install chromium webkit
make assets
make test             # Go/race/vet, 도구 단위, HTTP/Compose 통합
make test-ui          # 모의 UI/DOM
make test-e2e         # HTTP 브라우저, Chromium/WebKit 실제 뷰어
make test-backends    # 격리된 Docker WebDAV/FTP/SFTP
make test-docker      # 이미지 빌드 및 캐시·재시작 검사
# 전부: make test-all
```

Python 실행기를 바꾸려면 `make test PYTHON=/path/to/venv/bin/python`을 사용합니다. Go는 PATH에서 찾을 수 있어야 합니다. 개별 Python 검사는 `python3 -m tests.integration.http_smoke`처럼 모듈로 실행합니다. HTTP 계열 검사는 `tests.fixtures.s3`를 공유하며 다른 E2E 실행 파일을 import하지 않습니다.

검증한 범위와 운영 환경에서 남은 검사는 [검증 문서](../docs/VERIFICATION.md)에 구분합니다. 실제 운영 저장소나 사용자 `.env`는 테스트 데이터로 사용하지 않습니다.
