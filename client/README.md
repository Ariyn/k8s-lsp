# Kubernetes LSP Client

VS Code Client for Kubernetes LSP.

## Dev: auto-bump version on commit

This repo includes a Git hook that automatically bumps the patch version in
`client/package.json` after every commit (it creates an extra "bump version" commit).

One-time setup (from repo root):

```bash
./scripts/setup-githooks.sh
```

Notes:
- To bypass the auto-bump for a one-off commit: `K8S_LSP_SKIP_POST_COMMIT_BUMP=1 git commit ...`
- If you already staged `client/package.json` manually, the hook will not bump it.
