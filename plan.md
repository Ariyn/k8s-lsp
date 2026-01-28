# Semantic Tokens(하이라이팅) + “참조/정의” 시각화 구현 계획 (Schema 기반)

## 현재 상태(구현됨)
- Semantic Tokens: `textDocument/semanticTokens/full` 지원(기본 ON)
- Document Highlight: `textDocument/documentHighlight` 지원(기본 ON)
- Schema extension: `x-k8s-lsp-ref-*` 파싱 지원(RefMeta)
- 설정 토글: `k8sLsp.semanticTokens.enabled`, `k8sLsp.referencesVisualization.enabled`

## 목표
- YAML(Kubernetes 매니페스트)에서 **의미 기반 하이라이팅(Semantic Tokens)** 제공
- 커서 위치 기준으로 **정의/참조 관계를 시각화** (문서 내 하이라이트 + 선택적 링크/렌즈)
- **Schema 기반**으로 토큰 분류 및 참조/정의 판단을 일관되게 수행
- 기본 동작은 **ON(기본 활성화)**, 필요 시 설정으로 OFF 가능

## 비목표(초기 버전)
- 완전한 YAML AST 재작성/포매터
- Helm/템플릿 언어 구문까지 정확한 토큰화
- Cross-file reference highlight(문서 하이라이트는 문서 내로 제한; cross-file은 기존 Go to Definition/Peek References UI에 맡김)

---

## 기능 구성(1차/2차)

### 1차(필수)
1) **Semantic Tokens (Full)**
- YAML key/value + Kubernetes 구조 요소(apiVersion/kind/metadata.name 등) 하이라이팅
- “정의/참조”는 token modifier(예: `declaration`)로 표현

2) **Document Highlight**
- `textDocument/documentHighlight` 구현
- 커서가 정의/참조 위에 있을 때 같은 문서 내 occurrence를 하이라이트

3) **Refresh/무효화**
- 인덱스 업데이트/스키마 리로드 시 토큰 캐시 무효화 + `workspace/semanticTokens/refresh` 알림(가능하면)

### 2차(옵션, 기본 ON)
4) **DocumentLink 또는 CodeLens**
- 참조 값에 클릭 가능한 affordance 제공
  - DocumentLink: 값 위에 링크(클릭 시 정의로 이동/참조 보기)
  - CodeLens: “N references” 같은 표시
- 옵션은 기본 ON으로 제공하되, 설정으로 끌 수 있게 설계

---

## 설정(기본 ON)
VS Code 설정 키(제안):
- `k8sLsp.semanticTokens.enabled` (boolean, default: true)
- `k8sLsp.referencesVisualization.enabled` (boolean, default: true)
  - documentHighlight/documentLink/codelens 중 어떤 기능을 포함할지 세부 옵션으로 확장 가능
- `k8sLsp.referencesVisualization.links.enabled` (boolean, default: true)  // 2차
- `k8sLsp.referencesVisualization.codeLens.enabled` (boolean, default: false) // 노이즈 가능성 고려

서버는 `workspace/didChangeConfiguration`로 설정을 받아 capability는 켜두되(또는 refresh), 기능 실행 시 설정을 체크해 early-return.

---

## Schema 기반 접근 방식

### A) 토큰 분류를 Schema로 결정
- 현재 Registry(`pkg/schema.Registry`)에서 문서의 GVK에 해당하는 schema root를 얻음
- YAML AST를 순회하면서 “현재 경로(path)”를 추적하고, `schema.ResolvePath(root, path)`로 해당 위치의 schema node를 얻어 토큰 타입을 결정

예:
- `kind` 키는 `keyword`, `kind: Deployment` 값은 `type`
- `metadata.name` 값은 `variable` + `declaration`
- `metadata.namespace` 값은 `namespace`
- schema node의 `Enum`이 있으면 값 토큰을 `enumMember`(가능하면) 또는 `string` + modifier로 표시

### B) “참조”를 Schema 메타데이터로 표현(핵심)
현 `schema.Node`는 extension(사용자 정의 메타)을 담을 필드가 없음. 참조/정의 시각화를 “진짜 스키마 기반”으로 하려면 아래 중 하나를 채택.

**선호안(권장): schema.Node 확장 + OpenAPI extension 파싱**
1) `schema.Node`에 선택 필드 추가
- `Ref *RefMeta` (예: Kind, Group/Version, Scope(namespace/cluster), Role(reference/definition), Description)

2) `convertOpenAPIV3Schema`에서 아래 키를 파싱
- `x-k8s-lsp-ref-kind`: string (예: "ConfigMap", "Secret", "Service")
- `x-k8s-lsp-ref-scope`: string ("namespaced" | "cluster")
- `x-k8s-lsp-ref-role`: string ("reference" | "definition")

3) schema pack(사용자 제공 YAML)에서 위 extension을 지정 가능
- builtins/CRD는 기본적으로 extension이 없으므로, 1차에서는 “일부 well-known 경로”만 기본 매핑으로 제공

**대안: schema-path 매핑 테이블**
- GVK + JSONPath(또는 yaml path) 기반으로 참조필드를 미리 테이블화
- 장점: Node 구조 변경 없이 가능
- 단점: schema pack에서 확장하기 어렵고 유지보수 비용 증가

권장 방향은 “선호안”으로, schema packs를 통해 참조 의미를 확장할 수 있게 하는 것.

---

## LSP 구현 항목(서버)

### 1) Capabilities
- `ServerCapabilities.SemanticTokensProvider`
  - Legend: tokenTypes/tokenModifiers
  - Full 지원(필수), Delta는 성능 문제 발생 시 2차
- `ServerCapabilities.DocumentHighlightProvider` (documentHighlight)
- (옵션) `ServerCapabilities.DocumentLinkProvider` 또는 `CodeLensProvider`

### 2) 핸들러
- `TextDocumentSemanticTokensFull`
- `TextDocumentDocumentHighlight`
- (옵션) `TextDocumentDocumentLink` / `TextDocumentCodeLens`
- `WorkspaceSemanticTokensRefresh` notify (가능한 경우)

### 3) 캐시/성능
- 토큰 캐시 키: `(uri, docVersion, schemaVersionKey)`
  - schemaVersionKey는 “스키마 레지스트리 세대” 같은 단순 증가값으로 관리
- 문서 파싱은 기존 `yamlstream.Cache` 활용
- 토큰 생성은 AST 순회 1회, path tracking으로 schema resolve 호출 최소화

---

## Semantic Tokens 설계(legend + 규칙)

### tokenTypes(제안)
- `property` (YAML key)
- `keyword` (apiVersion/kind/metadata 같은 구조 키)
- `type` (Kind 값)
- `namespace` (namespace 값)
- `variable` (name 값 및 참조 값)
- `enumMember` (enum 값, 가능하면)
- `string` / `number` / `boolean` (schema.Type 기반)

### tokenModifiers(제안)
- `declaration` (정의)
- `readonly` (선택: 참조값을 읽기 의미로 표시)
- `defaultLibrary` (선택: built-in schema 기반 요소 표시용)

### 정확도 범위(초기)
- ScalarNode 단일 라인 값은 정확히 토큰화
- block scalar(`|`, `>`)는 전체를 `string`으로 단순 처리하거나 1차에서는 skip

---

## “참조/정의” 시각화 설계

### 1차: documentHighlight
- 커서가 위치한 node에 대해:
  1) YAML path + schema node resolve
  2) schema node가 `Ref` 메타를 갖고 있으면 reference/definition으로 분류
  3) 같은 문서에서 동일한 “참조 대상(예: kind+namespace+name)”를 갖는 occurrence를 찾아 highlights 반환

문서 내 검색 방법:
- 기존 resolver/indexer 로직을 재사용(가능하면): 문서에서 현재 위치의 참조를 추출하고, 동일 참조를 가진 다른 위치를 찾아 Location 범위 반환
- schema 기반일 때도 “어떤 값이 참조 대상인지”를 얻는 것은 schema.Ref로 결정

### 2차: documentLink / codeLens
- DocumentLink: 참조 값 위에 링크 제공
  - 클릭 시 정의로 이동(가능하면) 또는 references 뷰
- CodeLens: 정의 위치(예: metadata.name) 위에 “N references” 표시

기본값:
- links: ON
- codelens: OFF(노이즈 가능)

---

## 단계별 작업(마일스톤)

### Milestone 1 — Semantic Tokens Full
- [ ] legend 확정(tokenTypes/modifiers)
- [ ] `textDocument/semanticTokens/full` 구현
- [ ] schema 기반 토큰 매핑 1차(키/기본 구조/타입/enum)
- [ ] 토큰 캐시(URI+version) 적용

### Milestone 2 — Schema 기반 ref 메타데이터
- [ ] `schema.Node` 확장(RefMeta)
- [ ] `convertOpenAPIV3Schema`가 `x-k8s-lsp-ref-*` 파싱
- [ ] 최소 기본 매핑(Well-known 경로) 제공 + schema pack로 확장 가능 문서화

### Milestone 3 — documentHighlight
- [ ] `textDocument/documentHighlight` 구현
- [ ] schema.Ref 기반으로 같은 문서 occurrence highlight

### Milestone 4 — (옵션) DocumentLink
- [ ] documentLink로 참조값 클릭 affordance 제공
- [ ] 설정으로 ON/OFF 가능

### Milestone 5 — Refresh/무효화
- [ ] 스캔 완료/인덱스 변경/스키마 리로드 시 semanticTokensRefresh notify
- [ ] 토큰 캐시 무효화(스키마 세대 증가)

---

## 테스트/검증

### 단위테스트(Go)
- Semantic token 생성 결과가 안정적(라인/컬럼/길이)인지 검증
- schema.Ref extension 파싱 테스트(스키마 팩 YAML 샘플)
- documentHighlight가 예상 범위를 반환하는지 테스트

### 수동 검증(VS Code)
- 하이라이트 적용 확인(키/Kind/name/namespace/enum/type)
- 커서 이동 시 document highlight가 자연스럽게 켜지는지
- (옵션) 링크 클릭 → 정의/참조 탐색 흐름

---

## 리스크/대응
- YAML의 컬럼/길이 계산이 문자열 quoting/탭/멀티바이트에 민감
  - 1차는 단일 라인 scalar 중심, 필요 시 rune 기반 길이 계산 검토
- CRD/OpenAPI에서 ref 메타데이터가 기본 제공되지 않음
  - schema pack에 extension로 넣을 수 있게 하고, well-known 경로는 기본 제공
