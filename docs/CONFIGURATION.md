# Configuration

Kubernetes LSP의 설정을 한곳에서 정리한 레퍼런스입니다.

## Quick links
- 빠른 시작/Smoke Test: [README.md](../README.md)
- 문제 해결: [docs/TROUBLESHOOTING.md](TROUBLESHOOTING.md)
- 스키마 팩 포맷: [schemas/README.md](../schemas/README.md)

## Where to set

- UI: `Settings` → `Extensions` → `Kubernetes LSP`
- `settings.json`

설정 스코프 참고:
- `resource`: 워크스페이스(프로젝트)별
- `window`: VS Code 창 단위
- `machine`: PC/사용자 단위

## Quick recipes

### 1) 서버 바이너리 경로를 명시하기

서버 바이너리를 못 찾는 경우(또는 다른 버전으로 고정하고 싶은 경우) `k8sLsp.serverPath`를 지정합니다.

```json
{
	"k8sLsp.serverPath": "/absolute/path/to/k8s-lsp"
}
```

참고:
- 기본값이 `k8s-lsp`일 때, 확장은 실행 모드에 따라 서버를 자동 탐색합니다.
	- 개발 모드: 리포 루트의 `k8s-lsp`
	- 프로덕션 모드: 확장 내부 `bin/<platform>/<arch>/k8s-lsp(.exe)`

### 2) LSP 통신 로그 켜기 (문제 재현용)

```json
{
	"k8sLsp.trace.server": "messages"
}
```

- 출력은 Output 패널의 `Kubernetes LSP` 채널에서 확인합니다.
- 더 자세히 보고 싶으면 `verbose`를 사용합니다.

### 3) CRD 추가 로딩(예: Traefik)

```json
{
	"k8sLsp.crdSources": [
		"https://raw.githubusercontent.com/traefik/traefik/v3.0/docs/content/reference/dynamic-configuration/kubernetes-crd-definition-v1.yml"
	]
}
```

### 4) 추가 스키마 팩 로딩

```json
{
	"k8sLsp.schemaSources": [
		"./schemas/core.yaml",
		"./schemas/networking.yaml"
	]
}
```

참고:
- 서버는 기본으로 “서버 바이너리 옆의 `schemas/*.yaml`” 스키마 팩을 자동 로딩합니다.
- `k8sLsp.schemaSources`는 추가로 로딩하고 싶은 스키마 팩 소스를 넣는 용도입니다.

### 5) 성능 튜닝(편집 시 지연 조절)

```json
{
	"k8sLsp.diagnosticsDebounceMs": 200,
	"k8sLsp.indexDebounceMs": 250
}
```

- 값이 클수록 편집 중 CPU/디스크 부담이 줄고, 반응은 느려질 수 있습니다.
- `0`은 debounce 비활성입니다.

### 6) 포매팅 제어

```json
{
	"k8sLsp.formatting.enabled": true,
	"k8sLsp.formatting.indentSize": 2,
	"k8sLsp.formatting.disableForTemplates": true
}
```

## Settings reference

아래 목록은 VS Code 확장 `contributes.configuration` 기준(=사용자에게 노출되는 설정)입니다.

| Key | Scope | Type | Default | Description |
| --- | --- | --- | --- | --- |
| `k8sLsp.serverPath` | machine | string | `k8s-lsp` | `k8s-lsp` 실행 파일 경로 |
| `k8sLsp.crdSources` | resource | string[] | `[]` | 로드/인덱싱할 CRD YAML 소스 목록(https/file/file://) |
| `k8sLsp.schemaSources` | resource | string[] | `[]` | GVK별 OpenAPIV3 스키마를 제공하는 스키마 팩 소스 목록 |
| `k8sLsp.diagnosticsDebounceMs` | resource | number | `200` | 편집 후 diagnostics 발행 debounce(ms). `0`이면 비활성 |
| `k8sLsp.indexDebounceMs` | resource | number | `250` | 편집 후 재인덱싱 debounce(ms). `0`이면 비활성 |
| `k8sLsp.semanticTokens.enabled` | resource | boolean | `true` | 시맨틱 하이라이팅(semantic tokens) 활성화 |
| `k8sLsp.referencesVisualization.enabled` | resource | boolean | `true` | 문서 내 reference/definition 시각화(document highlights 등) |
| `k8sLsp.codeLens.enabled` | resource | boolean | `true` | schema-tagged reference 필드에 CodeLens 액션 표시 |
| `k8sLsp.documentLinks.enabled` | resource | boolean | `true` | schema-tagged reference 값을 클릭 가능한 링크로 제공 |
| `k8sLsp.formatting.enabled` | resource | boolean | `true` | 서버 기반 YAML 포매팅(Format Document) 활성화 |
| `k8sLsp.formatting.indentSize` | resource | number | `2` | 포매팅 들여쓰기 크기 |
| `k8sLsp.formatting.disableForTemplates` | resource | boolean | `true` | Helm 등 템플릿 문서에서 포매팅 보호(no-op) |
| `k8sLsp.trace.server` | window | string | `off` | VS Code ↔ 서버 통신 로그(`off`/`messages`/`verbose`) |

