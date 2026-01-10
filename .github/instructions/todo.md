# TODO

이 문서는 현재 프로젝트에 “추가하면 가치가 큰 기능”을 정리한 작업 목록입니다.
완료한 항목은 체크하고, 필요하면 세부 작업/링크를 추가합니다.

## P0 (체감 효과 큼)
- [ ] 워크스페이스 파일 변경 이벤트 반영
  - 목표: `WorkspaceDidChangeWatchedFiles`에서 Created/Changed 시 재인덱싱, Deleted 시 Store에서 제거까지 반영
  - 기대효과: Definition/References/Diagnostics 정확도 개선
  - 비고: 삭제 반영을 위해 파일→리소스 역인덱스(또는 Store에 filePath별 목록) 필요

- [ ] 다중 문서(`---`) 완전 지원
  - 목표: Validator도 `yaml.Decoder`로 파일 내 모든 문서를 순회하며 진단 생성
  - 기대효과: 실제 K8s 매니페스트(여러 리소스 1파일)에서 진단 누락 감소

- [ ] CRD 프리로드(다운로드) 서버 플로우 연동
  - 목표: 클라이언트 `initializationOptions.crdSources`를 받아 `pkg/crd`로 다운로드→인덱싱 수행
  - 기대효과: 커스텀 리소스도 즉시 인식(동적 kind 등록과 결합)

## P1 (UX 개선)
- [ ] Rename / Code Action(Quick Fix)
  - 예: 존재하지 않는 참조(Secret/ConfigMap/Service 등)에 대해 후보 제안 또는 생성 안내

- [ ] Document Symbol / Workspace Symbol
  - 목표: 파일/워크스페이스 내 K8s 리소스 목록을 심볼로 노출해 탐색성 향상

- [ ] 규칙/리졸빙 범위 확장
  - 목표: 자주 쓰는 참조 패턴(Helm/Argo/Kustomize 관용 패턴, selector 케이스 등) 추가

## P2 (성능/운영)
- [ ] 진단/인덱싱 디바운스 및 캐싱
  - 목표: `DidChange`에 대한 과도한 전체 파싱/검증을 줄이고 큰 파일에서도 반응성 유지

- [ ] 설정/로그 제어 확장
  - 목표: 로그 레벨, rules 경로, 스캔 exclude 패턴 등을 설정으로 노출
