# k8s-lsp (Kubernetes YAML Language Server)

VS Code에서 Kubernetes YAML을 더 빠르게 작성/탐색/리팩터링할 수 있게 해주는 **LSP 서버 + VS Code 클라이언트(확장)** 프로젝트입니다.

이 프로젝트를 설치하면, 쿠버네티스 매니페스트(YAML)에서 **리소스 간 참조를 따라 이동하고**, **사용처를 찾고**, **이름을 안전하게 변경**하고, **문서 구조를 빠르게 파악**할 수 있습니다.

## 설치하면 가능한 것 (핵심 기능)

### 1) 탐색: 정의로 이동 / 사용처 찾기
- **Go to Definition**: 참조 값(예: `secretName`, `configMap.name`, Ingress backend 등)에서 해당 리소스 정의 위치로 이동
- **Find References**: 특정 리소스(또는 참조 위치)의 사용처 목록 조회

### 2) 리팩터링: Rename / Code Actions
- **Rename**: 리소스 이름 및 참조를 함께 변경(가능한 범위에서 안전하게)
- **Code Actions**: 누락 리소스 생성, 참조 교체, selector/mismatch 수정 등(프로젝트 내 규칙 기반)

### 3) 작성 보조: Completion / Hover / Diagnostics
- **Completion**: YAML 작성 중 키/값 입력 보조(규칙/스키마 기반으로 확장 중)
- **Hover**: 필드/참조에 대한 컨텍스트 정보 표시
- **Diagnostics**: 규칙 기반 검증 및 오류/경고 표시(편집 중 debounce로 성능 안정화)

### 4) 구조 파악: Symbols
- **Document Symbols** / **Workspace Symbols** 제공

### 5) Embedded 콘텐츠 편집 (가상 문서)
- YAML 안에 “임베디드된 파일/콘텐츠”를 **가상 문서로 열고 저장**할 수 있습니다.
- `k8s-embedded:` 스킴을 VS Code의 FileSystemProvider로 제공하며, 서버에 `workspace/executeCommand`로 내용을 요청/저장합니다.

### 6) 스키마 팩(내장/추가) 로딩
- 서버는 실행 시, 서버 바이너리 옆의 `schemas/*.yaml` 스키마 팩을 자동 로딩합니다.
- 추가 스키마 팩은 VS Code 설정 `k8sLsp.schemaSources`로 등록할 수 있습니다.
- 스키마 팩 포맷은 [schemas/README.md](schemas/README.md)를 참고하세요.

## 설치 방법 (VS Code)

이 리포는 **VS Code 확장(VSIX)** 로 패키징해서 설치하는 방식이 가장 간단합니다.

### A) 릴리즈 VSIX 설치(권장)
- GitHub Releases에서 `k8s-lsp-client.vsix`를 내려받아 VS Code에 설치합니다.
  - VS Code: `Extensions` → `...` → `Install from VSIX...`

### B) 로컬에서 직접 빌드/패키징
요구사항:
- Go `1.25+`
- Node.js `20+`

리포 루트에서:
- `./package.sh`

성공하면 리포 루트에 `k8s-lsp-client.vsix`가 생성됩니다.

## (메인테이너) 자동 배포: VS Code Marketplace

- 태그 `v*`를 push 하면 GitHub Actions가 VSIX를 빌드하고, `VSCE_PAT` 시크릿이 설정되어 있으면 VS Code Marketplace로 자동 publish 합니다.
- 수동 실행이 필요하면 Actions에서 `Release Extension` 워크플로를 `workflow_dispatch`로 실행하고 `publish=true`를 선택합니다.

필요 시크릿:
- `VSCE_PAT`: Visual Studio Marketplace Personal Access Token (publisher: `k8s-lsp` 권한 필요)

## 사용 방법 (빠른 시작)

### 60초 Quickstart

1) VSIX 설치 후, 워크스페이스에서 `.yaml`/`.yml` 파일을 엽니다(activation: `onLanguage:yaml`).
2) Output 패널에서 `Kubernetes LSP` 채널을 열어 서버가 정상 기동했는지 확인합니다.
3) 아래 동작 확인(Smoke Test) 중 1~2개를 실행해 봅니다.

### 동작 확인 (Smoke Test)

- Completion: `apiVersion:` / `kind:` / `metadata:` 입력 중 자동완성 제안이 뜨는지
- Hover: 필드/참조 값 위에 올렸을 때 추가 정보가 표시되는지
- Diagnostics: 잘못된 필드를 넣었을 때 경고/에러가 뜨는지
- Go to Definition / Find References: 참조 값에서 대상 리소스 정의로 이동/사용처 찾기가 되는지
- Formatting: `Format Document`가 동작하는지(템플릿 문서는 기본적으로 보호)

### 문제가 있을 때 가장 먼저 볼 것

1) Output → `Kubernetes LSP` 채널 로그
2) `k8sLsp.serverPath` 설정이 올바른지(서버 바이너리를 못 찾으면 확장이 에러를 띄우고, 로그에 탐색한 경로가 출력됩니다)
3) 필요하면 `k8sLsp.trace.server`를 `messages` 또는 `verbose`로 올려 LSP 통신 로그를 확인합니다.

더 자세한 설정/트러블슈팅은 아래 문서를 참고하세요.
- 설정: [docs/CONFIGURATION.md](docs/CONFIGURATION.md)
- 문제 해결: [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md)

## 설정 (VS Code)

설정 경로: `Settings` → `Extensions` → `Kubernetes LSP` (또는 `settings.json`에 직접 추가)

주요 설정:
- `k8sLsp.serverPath` (기본: `k8s-lsp`)
  - 서버 바이너리 경로를 직접 지정합니다.
  - 확장이 서버 바이너리를 찾지 못하면 가장 먼저 이 설정을 확인하세요.
- `k8sLsp.crdSources` (기본: `[]`)
  - CRD YAML 소스 목록(`https://`, 파일 경로, `file://` URI)
- `k8sLsp.schemaSources` (기본: `[]`)
  - 추가 스키마 팩 소스 목록(`https://`, 파일 경로, `file://` URI)
- `k8sLsp.diagnosticsDebounceMs` (기본: `200`)
  - 편집 중 진단 발행 debounce(ms). `0`이면 비활성
- `k8sLsp.indexDebounceMs` (기본: `250`)
  - 편집 중 인덱싱 debounce(ms). `0`이면 비활성
- `k8sLsp.formatting.enabled` (기본: `true`)
  - 서버 기반 YAML 포매팅(Format Document) 활성화
- `k8sLsp.formatting.indentSize` (기본: `2`)
  - 포매팅 들여쓰기 크기
- `k8sLsp.formatting.disableForTemplates` (기본: `true`)
  - Helm 등 템플릿 신호가 있는 문서에서 포매팅을 no-op 처리
- `k8sLsp.trace.server` (기본: `off`)
  - VS Code ↔ 언어 서버 간 통신 로그를 출력합니다(`messages`/`verbose`).

예시(`settings.json`):
```json
{
  "k8sLsp.diagnosticsDebounceMs": 200,
  "k8sLsp.indexDebounceMs": 250,
  "k8sLsp.crdSources": [
    "https://raw.githubusercontent.com/traefik/traefik/v3.0/docs/content/reference/dynamic-configuration/kubernetes-crd-definition-v1.yml"
  ],
  "k8sLsp.schemaSources": [
    "./schemas/core.yaml",
    "./schemas/networking.yaml"
  ]
}
```

## 제공 커맨드

확장은 아래 커맨드를 제공합니다(커맨드 팔레트에서 실행 가능).
- `Kubernetes LSP: Open Embedded File` (`k8sLsp.openEmbeddedFile`)
- `Kubernetes LSP: Find Embedded File Usages` (`k8sLsp.findEmbeddedFileUsages`)
- `Kubernetes LSP: Show subPath Targets` (`k8sLsp.showSubPathTargets`)

## 제한/주의사항

- YAML 템플릿(Helm 등)처럼 **유효한 YAML이 아닌 문서**는 파싱/진단이 제한될 수 있습니다.
- 스키마 기반 구조 검증/정교한 값 completion은 로드맵에 있으며, 현재는 rules/pack 기반 기능이 중심입니다.

## 개발자용(리포 구조)

- 서버(Go): 리포 루트의 `main.go` 및 `pkg/*`
- 클라이언트(VS Code 확장): [client](client) 폴더
- 규칙(rules): [rules](rules)
- 스키마 팩: [schemas](schemas) (포맷: [schemas/README.md](schemas/README.md))

클라이언트 개발 관련은 [client/README.md](client/README.md)를 참고하세요.

## 라이선스

- 서버: [LICENSE.md](LICENSE.md)
- 클라이언트: [client/LICENSE.md](client/LICENSE.md)
