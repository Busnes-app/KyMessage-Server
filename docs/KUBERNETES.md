# Kubernetes

[Product overview](../README.md) · [Standalone installation](../INSTALL.md)

Commands and unlinked file paths below are relative to the repository root.

### Kubernetes (Ky cluster)

The K80/K81 deployment lives in `busnes.app/ky-kubernetes` (manifests, `setup-kymessages.sh`
wizard and runbook; not versioned). Design record:
`docs/superpowers/specs/2026-10-02-k8s-deploy-design.md`. It runs the same images and
`matrix-init` output; there too the app runs as `KY_MATRIX_UID`, and Element is
restarted with `kubectl -n ky-stack rollout restart deployment/element`
(`KY_MATRIX_ELEMENT_RESTART_HINT`).

A cluster Synapse started with only `homeserver.yaml` keeps running, closed, and ignores
Settings > Federation. To let the console open federation there, give Synapse the args `run --config-path /config/homeserver.yaml --config-path
/federation.yaml` and a read-only subPath mount of `federation/federation.yaml` at
`/federation.yaml`; give the app a writable subPath mount of the same file at
`/matrix/federation/federation.yaml`; set `KY_MATRIX_SYNAPSE_RESTART_HINT` to
`kubectl -n ky-stack rollout restart deployment/synapse`. Opening federation also needs
Synapse egress to public addresses on 443 and 8448 and the apex `/.well-known/matrix/server`
route; closed, neither is needed.
