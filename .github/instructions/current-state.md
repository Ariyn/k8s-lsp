# 현재 상태 (2026-01-10)

## 실행 환경
- OS: Linux
- 워크스페이스 루트: `/home/admin/repo/k8s-lsp`
- 현재 열려있는 파일: `/home/admin/repo/k8s-lsp/.github/instructions/current-state.md`

## 프로젝트 개요
이 레포는 **Kubernetes YAML을 대상으로 하는 LSP 서버(Go)** 와 **VS Code 클라이언트 확장(Typescript)** 으로 구성되어 있습니다.

- 서버(Go): [main.go](../../main.go)
  - GLSP 기반 LSP 서버로 stdio로 동작
  - 인덱싱(Store) + 참조/정의/호버/완성 + 진단(Validation)
- 클라이언트(VS Code Extension): [client/src/extension.ts](../../client/src/extension.ts)
  - 서버 바이너리 실행 및 LSP 연결
  - 임베디드 문서(예: ConfigMap data의 파일)용 가상 파일시스템 제공

## 현재 구현 상태(기능 기준)

### 1) 서버: LSP 기능
서버는 초기화 시 다음 capability를 제공합니다(Full sync 기반).

- 문서 동기화: `TextDocumentSyncKindFull`
- Go-to Definition: 구현됨
- Find References: 구현됨
- Completion: 구현됨(설정된 규칙 기반)
- Hover: 구현됨(일반 참조 + ConfigMap 임베디드 파일 링크)
- Execute Command: 구현됨
  - `k8s.embeddedContent` : 임베디드 파일 내용 읽기
  - `k8s.saveEmbeddedContent` : 임베디드 파일 내용 저장(WorkspaceEdit 반환)

관련 코드:
- LSP 핸들러/핵심 플로우: [main.go](../../main.go)
- 해석(Definition/References/Hover/Embedded): [pkg/resolver/resolver.go](../../pkg/resolver/resolver.go)
- 완성(Completion): [pkg/resolver/completion.go](../../pkg/resolver/completion.go)

### 2) 인덱싱(Store) 및 동작 방식
워크스페이스의 YAML을 파싱하여 “리소스 정의”와 “참조”를 추출해 메모리 Store에 저장합니다.

- 워크스페이스 스캔:
  - 초기화 완료 후 비동기로 실행
  - `.git` 같은 숨김 디렉토리는 스킵
  - `*.yaml`/`*.yml`만 대상으로 인덱싱
- 동적 업데이트:
  - `DidOpen`/`DidChange` 시 열린 문서 내용도 즉시 인덱싱
  - Definition/References 요청 시 스캔이 아직 끝나지 않았으면 **최대 ~1.5초 대기 후 1회 재시도**
- Store 키:
  - `Kind/Namespace/Name` (namespace 비어있으면 `default`로 간주)

관련 코드:
- 스캔/인덱싱: [pkg/indexer/indexer.go](../../pkg/indexer/indexer.go)
- 저장소(Store): [pkg/indexer/store.go](../../pkg/indexer/store.go)

### 3) 규칙 기반 심볼/참조(Go-to Definition/References/Completion의 기반)
규칙은 `rules/*.yaml`에서 로드되며, 인덱서/리졸버가 동일한 규칙 개념을 공유합니다.

- 심볼 정의(리소스 이름, 라벨 정의 등): [rules/k8s.yaml](../../rules/k8s.yaml)
  - 예: `metadata.name`를 리소스 정의로 인덱싱
  - 예: `metadata.labels`, `spec.template.metadata.labels`를 라벨 정의로 인덱싱
- 참조 규칙(서비스 셀렉터, ingress backend service, envFrom configMap/secret 등): [rules/k8s.yaml](../../rules/k8s.yaml)
  - Completion은 “특정 path에서 참조되는 targetKind 목록”을 Store에서 뽑아 제공

### 4) CRD(동적 Kind) 처리
CRD YAML이 인덱싱되면, 그 안의 `spec.names.kind`를 읽어 **동적으로 kind를 등록**합니다.

- 구현됨: `CustomResourceDefinition` 인덱싱 시 dynamic kind 등록
- 효과: 이후 해당 kind도 `metadata.name` 기반 리소스로 인덱싱 가능

관련 코드:
- CRD kind 등록: [pkg/indexer/indexer.go](../../pkg/indexer/indexer.go)

추가로, URL에서 CRD를 다운로드하여 캐시에 저장하는 기능이 패키지로 존재합니다.

- 구현됨(패키지 단위): [pkg/crd/downloader.go](../../pkg/crd/downloader.go), [pkg/crd/index.go](../../pkg/crd/index.go)
- 현재 상태(연동): **서버 시작 플로우에서 다운로드/프리로드를 호출하는 연결은 아직 없음**

### 5) ConfigMap 임베디드 파일(가상 문서) 지원
ConfigMap의 `data`/`binaryData`에서 “파일명처럼 보이는 키(점 `.` 포함)”이고 값이 블록 스타일(`|` 또는 `>`)인 경우,
해당 키를 **가상 URI(`k8s-embedded://...`)** 로 열고 편집할 수 있게 지원합니다.

- Hover:
  - “Open File / Find Usages” 링크를 Markdown으로 제공
- Definition:
  - ConfigMap 키에서 Go-to Definition 시 임베디드 문서로 점프
- Save:
  - 임베디드 문서 저장 시 원본 YAML에 반영하는 WorkspaceEdit 생성

관련 코드:
- 서버 executeCommand/저장 로직: [main.go](../../main.go)
- 임베디드 해석/텍스트 편집 생성: [pkg/resolver/resolver.go](../../pkg/resolver/resolver.go)
- VS Code 가상 파일시스템: [client/src/virtualDocumentProvider.ts](../../client/src/virtualDocumentProvider.ts)

### 6) 추가 리졸빙(특수 케이스)
- `containers[].volumeMounts[].name` → `spec.template.spec.volumes[].name`로 Go-to Definition 지원
  - (Deployment 등 workload 내 볼륨/마운트 이름 연결)

관련 코드:
- 볼륨 마운트 특수 처리: [pkg/resolver/resolver.go](../../pkg/resolver/resolver.go)

### 7) 진단(Validation)
`rules/validation.yaml` 기반으로 일부 교차 리소스 검증을 수행하고 `publishDiagnostics`로 경고를 표시합니다.

- 구현된 체크 유형:
  - `reference`: 참조 대상 리소스 존재 여부(또는 셀렉터가 어떤 리소스와도 매칭되지 않음)
  - `resource-match`: PVC ↔ PV 속성 일치(예: capacity, accessModes)

관련 코드:
- 검증기: [pkg/validator/validator.go](../../pkg/validator/validator.go)
- 규칙: [rules/validation.yaml](../../rules/validation.yaml)

## 클라이언트(VS Code 확장) 구현 상태
- 서버 실행/연결:
  - `k8sLsp.serverPath` 설정값 또는 기본 경로로 서버 바이너리 탐색
  - 개발 모드에서는 워크스페이스 루트의 `k8s-lsp` 바이너리를 우선 사용
- 초기화 옵션 전달:
  - `k8sLsp.crdSources` 배열을 `initializationOptions.crdSources`로 서버에 전달(현재 서버는 값 수신만 함)
- 임베디드 파일 UX:
  - `k8s-embedded://` 스킴을 FileSystemProvider로 열어서 편집 가능
  - Hover에서 커맨드 링크를 “trusted markdown”으로 허용
- 추가 UX:
  - `subPath: ...` 패턴을 DocumentLink로 만들어 참조 결과를 바로 보여주는 커맨드 호출

관련 코드:
- 확장 엔트리: [client/src/extension.ts](../../client/src/extension.ts)
- 가상 FS: [client/src/virtualDocumentProvider.ts](../../client/src/virtualDocumentProvider.ts)

## 빌드/터미널 상태(스냅샷)
- 마지막 실행 명령: `cd /home/admin/repo/k8s-lsp/client && npm run compile`
- 마지막 종료 코드: `0` (성공)
- Go 빌드/테스트 실행 여부: 이 문서 생성 시점 기준으로 캡처되지 않음

## 제약/미완(현재 코드 기준)
- 워크스페이스 파일 변경 감지:
  - `WorkspaceDidChangeWatchedFiles`에서 이벤트를 로그만 남기고 실제 재인덱싱/삭제 반영은 TODO
- Store 정리(삭제/이동):
  - 파일 삭제 시 해당 파일에서 인덱싱된 리소스를 제거하는 로직이 없음(파일→리소스 역인덱스 필요)
- Validation의 다중 문서 처리:
  - 현재 `yaml.Unmarshal` 기반이라 단일 문서 중심으로 동작(파일 내 `---` 다중 문서 전체 검증은 제한적)
- CRD 다운로드 프리로드:
  - 다운로드/캐시/인덱싱 유틸은 있으나 서버 부팅 플로우에 아직 연결되어 있지 않음
