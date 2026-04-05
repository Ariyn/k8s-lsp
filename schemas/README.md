# k8s-lsp schema packs

This folder contains YAML schema packs that k8s-lsp can load at startup.

- The server loads `schemas/*.yaml` next to the server binary automatically.
- You can also add more packs via the VS Code setting `k8sLsp.schemaSources`.

Suggested packs:

- `core.yaml`: core/apps/batch/rbac/storage/etc.
- `networking.yaml`: Ingress/NetworkPolicy/IngressClass + Gateway API.

## Format

Each YAML document registers an OpenAPIV3 schema for a Kubernetes GVK.

```yaml
group: "apps"
version: "v1"
kind: "Deployment"
openAPIV3Schema:
  type: object
  properties:
    spec:
      type: object
      properties:
        replicas: { type: integer }
  additionalProperties: true
```

Multi-doc YAML is supported (`---`).

Note: avoid YAML anchors/aliases in schema packs for now.
