> 이 문서는 기존 목록/ZIP 전달 구조 설명입니다. 0.5.0 내장 미리보기·CORS 변경은 [PREVIEW.md](PREVIEW.md)를 참고하세요.

# 0.4.0 — 화면 목록과 ZIP 재귀 조회의 구분

## 이전 0.3.0에 이미 반영된 것

검색창/필터는 제거되어 있습니다. 전체 파일명 인덱스를 새로 추가하지 않았습니다.

화면에서 폴더를 열 때에는 `ListObjectsV2(prefix=현재경로, delimiter=/, max-keys=1000)`을 요청합니다. S3의 CommonPrefixes 그룹 하나는 응답 한 항목으로 계산됩니다. 하위 폴더에 파일 10,000개가 있어도 상위 폴더에서는 그 폴더 한 항목입니다. 하위 파일을 모두 나열한 뒤 클라이언트에서 숨기는 방식이 아닙니다. 파일 본문은 받지 않습니다.

현재 폴더의 직접 파일과 바로 아래 폴더를 다음 페이지가 없을 때까지 ‘더 불러오기’로 조회할 수 있습니다. 최대 1,000개는 **각 API 응답의 상한**입니다. S3는 더 적은 결과를 반환할 수도 있으므로, 개수로 완료를 추측하지 않고 IsTruncated/NextContinuationToken을 따릅니다. 현재 UI는 다음 페이지를 자동 순회하지 않고 정렬도 불러온 항목에만 적용합니다.

## 이번 0.4.0에서 추가한 것

폴더 체크박스와 **선택 폴더의 재귀 ZIP**입니다. ZIP 준비 시에만 선택 prefix에 delimiter를 지정하지 않고 모든 페이지의 메타데이터를 읽습니다. 폴더를 열어보거나 하위 파일을 UI에 미리 불러올 필요가 없습니다. ZIP에는 현재 폴더 기준 상대 경로를 유지합니다. 0바이트 폴더 마커로 표시된 빈 폴더도 보존합니다.

직접 선택한 파일은 HEAD, 폴더에서 재귀 조회한 파일은 원본 LIST 응답으로 크기·ETag를 확인합니다. 본문은 ZIP 스트리밍 시에만 읽습니다. 파일 수·합계 용량 제한은 재귀 확장 후 전체에 적용하며 넘으면 일부 ZIP을 만들지 않고 요청을 거부합니다. 기본은 200개·20GiB·동시 2개이고 파일 수 설정 범위는 1–10,000입니다. 준비 제한은 60초입니다.

presign은 **GetObject(GET)만** 허용하도록 바꿨습니다. 이전 버전은 HEAD presign도 허용했습니다. 이제 HEAD는 객체 서버 경로로, 목록은 서버의 ListObjectsV2로 처리합니다. 개별 다운로드/미리보기 GET은 기존 ENV에서 독립적으로 proxy/presigned를 선택합니다. ZIP 자체는 언제나 서버를 경유합니다.

## 구현 위치

- `internal/s3/s3.go`: 기존 한 단계 폴더 목록 (`delimiter=/`).
- `internal/s3/walk.go`: ZIP 전용 재귀 prefix 메타데이터 순회 (delimiter 생략).
- `internal/server/archive_plan.go`: 선택 경로·재귀 전체 수/크기·경로 안전성·메타데이터 계획.
- `internal/server/archive.go`: 일회용 계획·Store ZIP 스트리밍·폴더 마커·취소/실패 처리.
- `internal/s3/presign.go`, `internal/server/server.go`: GET 전용 presign, HEAD 서버 처리, 독립 모드 설정.
- `web/app.js`: 간단한 파일/폴더 선택과 네이티브 ZIP 다운로드.
- `internal/s3/listing_test.go`, `internal/server/archive_recursive_test.go`, `internal/s3/presign_test.go`: 한 단계 목록과 재귀 ZIP, GET 전용 서명의 회귀 검사.

API 계약 근거: AWS ListObjectsV2 공식 문서의 Delimiter/CommonPrefixes/NextContinuationToken 항목.
https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListObjectsV2.html
https://docs.aws.amazon.com/AmazonS3/latest/userguide/using-prefixes.html

전체 설정은 README, 실제 실행 범위는 VERIFICATION.md를 참고하세요.

## 단독 실행과 선택 캐시

기본 `compose.yaml`은 mori 서비스 하나뿐이며 `BROWSER_PROXY_URL`의 기본값은 빈 값입니다. 외부 캐시 서버 없이 S3에서 직접 메타데이터와 객체 본문을 읽을 수 있습니다. `proxy` 모드에서는 본문이 S3 → mori → 브라우저로 이동하고, `presigned`의 개별 GetObject는 브라우저가 S3에서 받습니다. ZIP은 어떤 모드에서도 mori가 직접 묶어 스트리밍합니다.

파일 목록 TTL 메모리 캐시는 mori 내부에 있습니다. 파일 본문 디스크/세그먼트 캐시는 내장하지 않았으며 `compose.cache.yaml`로 별도 서비스가 필요할 때만 추가합니다. `CACHE_*`는 그 별도 서비스의 설정입니다. 기본 배포는 추가 프록시를 빌드하거나 기다리지 않습니다.
