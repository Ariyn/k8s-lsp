# Kubernetes LSP Master Plan

이 문서는 Kubernetes YAML 파일을 위한 Language Server Protocol (LSP) 서버 구현을 위한 마스터 플랜입니다.
핵심 목표는 **Go to Definition(정의로 이동)**과 **Go to Usage(사용처 찾기)** 기능을 구현하는 것입니다.

## 1. 프로젝트 개요 (Project Overview)
- **목표**: Kubernetes 리소스 간의 관계를 이해하고 탐색할 수 있는 LSP 서버 개발.
- **핵심 기능**:
  - **Go to Definition**: 리소스 참조(예: Service의 selector)에서 정의(예: Deployment의 label)로 이동.
  - **Find References (Go to Usage)**: 리소스 정의에서 해당 리소스를 참조하는 모든 곳을 찾기.
- **대상**: VS Code 및 LSP를 지원하는 모든 에디터.

## 2. 기술 스택 (Tech Stack)
- **Language**: Go (Golang)
- **Libraries**:
  - **LSP Framework**: `github.com/tliron/glsp` (또는 `github.com/sourcegraph/go-lsp`) - LSP 프로토콜 구현체.
  - **YAML Parser**: `gopkg.in/yaml.v3` - YAML 파싱 및 AST(Node) 접근 (Line/Column 정보 포함 필수).
  - **Logging**: `github.com/rs/zerolog` (선택 사항, 구조화된 로깅).

## 3. 아키텍처 (Architecture)

### 3.1. Server (LSP Handler)
- `glsp` 핸들러를 통해 클라이언트 요청(`Initialize`, `TextDocumentDefinition`, `TextDocumentReferences`) 처리.
- JSON-RPC 통신 관리.

### 3.2. Indexer (Workspace Scanner)
- 워크스페이스 내의 모든 YAML 파일을 스캔 (`filepath.Walk` 등 사용).
- 각 파일의 K8s 리소스 정보(`apiVersion`, `kind`, `metadata.name`, `metadata.namespace`)를 추출하여 인메모리 맵(Store)에 저장.
- 파일 변경(`textDocument/didChange`, `didOpen`, `didSave`) 시 인덱스 업데이트.

### 3.3. Parser (YAML AST)
- `yaml.v3`의 `yaml.Node`를 사용하여 문서를 트리 구조로 파싱.
- 커서 위치(Line, Character)가 AST의 어떤 노드에 해당하는지 찾는 로직 구현.

### 3.4. Resolver (Relationship Logic)
- K8s 리소스 간의 연결 고리를 정의.
- 예: Service Selector <-> Pod Labels 매칭 로직.

## 4. 구현 단계 (Implementation Phases)

### Phase 1: 프로젝트 셋업 및 기본 LSP 구조 (Setup) - [Completed]
- [x] Go 모듈 초기화 (`go mod init`).
- [x] `glsp` 및 `yaml.v3` 의존성 설치.
- [x] 기본 LSP 서버 진입점(`main.go`) 작성 및 Stdio 통신 확인.
- [x] 클라이언트(VS Code Extension 등)와 연결 테스트 (로그 확인 완료).

### Phase 2: YAML 파싱 및 인덱싱 (Indexing) - [Completed]
- [x] 워크스페이스 스캔 로직 구현 (`pkg/indexer/indexer.go`).
- [x] `yaml.v3`를 사용하여 리소스 메타데이터 파싱.
- [x] `Store` (In-memory DB) 구조체 구현: `Kind`, `Name`으로 리소스 위치 조회 (`pkg/indexer/store.go`).
- [x] `main.go`에 인덱서 통합 및 초기화 시 스캔 실행.

### Phase 3: Go to Definition 구현 - [Completed]
- [x] `textDocument/definition` 핸들러 구현 (`main.go`).
- [x] `Resolver` 로직 구현 (`pkg/resolver/resolver.go`).
- [x] `Store`에 `FindByLabel` 추가 및 `K8sResource`에 Labels 필드 추가.
- [x] **지원 관계**:
  - [x] Service -> Workload (Selector)
  - [x] Ingress -> Service

### Phase 4: Go to Usage (References) 구현
- `textDocument/references` 핸들러 구현.
- 역방향 검색: 특정 리소스(예: ConfigMap)가 정의된 곳에서 호출 시, 이를 참조하는 파일 위치 반환.

### Phase 5: 설정 구현

- rules 하위의 .yaml 파일을 모두 읽고, 설정으로 사용한다. 내부의 예시는 아래와 같다.
```yaml
version: 1
symbols:
  - name: k8s.resource.name
    description: "리소스 이름 (kind + namespace + name)"
    keyTemplate: "{{ .kind }}:{{ .namespace }}:{{ .name }}"
    definitions:
      - kinds: ["Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob", "Pod"]
        path: "metadata.name"
      - kinds: ["Service", "Ingress", "ConfigMap", "Secret", "PersistentVolumeClaim"]
        path: "metadata.name"

references:
  - name: service.selector.label
    symbol: k8s.label
    match:
      kinds: ["Service"]
      path: "spec.selector"

  - name: ingress.backend.service
    symbol: k8s.resource.name
    match:
      kinds: ["Ingress"]
      path: "spec.rules[].http.paths[].backend.service.name"

  - name: deployment.configmap-ref
    symbol: k8s.resource.name
    match:
      kinds: ["Deployment"]
      path: "spec.template.spec.volumes[].configMap.name"

symbols:
  - name: k8s.label
    description: "레이블 (namespace + key + value)"
    keyTemplate: "{{ .namespace }}:{{ .key }}={{ .value }}"
    definitions:
      - kinds: ["Deployment", "StatefulSet", "DaemonSet", "Job", "Pod"]
        path: "metadata.labels"
```

이를 통해 definition과 go to usage를 찾으며, 하드코딩하지 않는다.

### Phase 6: 테스트 및 최적화
- Go Test를 이용한 파서 및 리졸버 유닛 테스트.
- 동시성 처리 (Goroutine) 및 대용량 파일 처리 성능 최적화.

## 5. 상세 로직 예시

### 5.1 설정 위주의 작업

### 시나리오: Service에서 Deployment로 이동
1. 사용자가 Service YAML의 `selector: app: my-app`에서 `my-app`을 클릭.
2. **Server**: 커서 위치(Line, Col)를 받아 `yaml.Node` 트리 탐색 -> 현재 노드가 `spec.selector`의 Value임을 확인.
3. **Resolver**: `app: my-app` 레이블을 가진 Pod(Deployment)를 인덱스 스토어에서 검색.
4. **Result**: 매칭되는 Deployment 파일의 `metadata.labels` 위치(`Location` 구조체) 반환.
