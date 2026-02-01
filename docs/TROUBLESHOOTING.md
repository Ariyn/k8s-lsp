# Troubleshooting

이 문서는 Kubernetes LSP가 동작하지 않을 때의 진단 동선을 제공합니다.

현재는 Phase 1에서 링크가 깨지지 않도록 스텁으로 추가되어 있으며, 케이스별 해결책은 Phase 3에서 확장됩니다.

## First checks
1. VS Code Output 패널에서 `Kubernetes LSP` 채널을 확인합니다.
2. 에러 메시지에 `k8s-lsp server binary not found at: ...`가 나오면 `k8sLsp.serverPath` 설정을 확인합니다.
3. 통신 로그가 필요하면 `k8sLsp.trace.server`를 `messages` 또는 `verbose`로 설정합니다.

## Quick links
- 빠른 시작/Smoke Test: [README.md](../README.md)
- 설정 레퍼런스: [docs/CONFIGURATION.md](CONFIGURATION.md)
