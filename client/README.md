# Kubernetes LSP Client (VS Code 확장)

Kubernetes YAML을 작성할 때 **참조 이동 / 사용처 찾기 / Rename 리팩터링 / 진단(오류/경고) / 스키마 기반 도움말**을 제공하는 VS Code 확장입니다.

이 확장은 단독으로 동작하지 않고, 함께 설치되는 **k8s-lsp 언어 서버(Language Server)** 와 통신하여 기능을 제공합니다.

## 무엇을 해주나요?

- **Go to Definition / Peek Definition**: YAML 안의 참조 값에서 대상 리소스 정의로 이동
- **Find References**: 리소스/참조의 사용처 목록 조회
- **Rename**: 리소스 이름과 관련 참조를 가능한 범위에서 함께 변경
- **Diagnostics**: 규칙/스키마 기반으로 오류/경고 표시(편집 중 debounce로 성능 안정화)
- **Formatting**: 서버 기반 YAML 포매팅(템플릿 문서는 기본적으로 보호)

## 빠른 시작

1) 확장을 설치합니다.
2) 워크스페이스에서 `.yaml` / `.yml` 파일을 열면 자동 활성화됩니다.
3) 참조 필드 위에서 `F12`(정의로 이동), `Shift+F12`(참조 찾기), `F2`(이름 바꾸기)를 사용해 보세요.

## 제공 커맨드

커맨드 팔레트에서 실행:

- `Kubernetes LSP: Open Embedded File` (`k8sLsp.openEmbeddedFile`)
- `Kubernetes LSP: Find Embedded File Usages` (`k8sLsp.findEmbeddedFileUsages`)
- `Kubernetes LSP: Show subPath Targets` (`k8sLsp.showSubPathTargets`)
- `Kubernetes LSP: Peek Definition` (`k8sLsp.peekDefinition`)
- `Kubernetes LSP: Go to Definition` (`k8sLsp.goToDefinition`)

## 설정

VS Code 설정에서 `Kubernetes LSP` 섹션을 찾거나, `settings.json`에 직접 추가할 수 있습니다.

자주 쓰는 설정:

- `k8sLsp.serverPath`: `k8s-lsp` 실행 파일 경로(서버를 못 찾는 경우 우선 확인)
- `k8sLsp.crdSources`: 인덱싱할 CRD YAML 소스 목록(https/file/file://)
- `k8sLsp.schemaSources`: 추가 스키마 팩 소스 목록(https/file/file://)
- `k8sLsp.diagnosticsDebounceMs`: 편집 후 진단 발행 지연(ms)
- `k8sLsp.indexDebounceMs`: 편집 후 인덱싱 지연(ms)
- `k8sLsp.formatting.enabled`: 포매팅 활성화
- `k8sLsp.formatting.disableForTemplates`: Helm 등 템플릿 문서에서 포매팅 보호

예시:

```json
{
	"k8sLsp.serverPath": "k8s-lsp",
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

## 문제 해결

- 기능이 동작하지 않으면 먼저 `k8sLsp.serverPath`가 올바른지 확인하세요.
- 로그는 VS Code의 Output 패널에서 언어 서버 채널(또는 확장 출력)을 확인하세요.

## 더 자세한 설명

프로젝트 전체(서버/클라이언트/스키마 팩/패키징)는 리포지토리 루트 README를 참고하세요.

---

## 개발자용: 커밋 시 버전 자동 증가(패치)

이 리포에는 커밋 후 `client/package.json`의 patch 버전을 자동으로 올리는 Git hook이 포함되어 있습니다(추가로 "bump version" 커밋이 생성됨).

1회 설정(리포 루트에서 실행):

```bash
./scripts/setup-githooks.sh
```

참고:
- 한 번만 우회하고 싶으면: `K8S_LSP_SKIP_POST_COMMIT_BUMP=1 git commit ...`
- 이미 `client/package.json`을 스테이징 해둔 경우, hook이 자동 증가를 건너뜁니다.
