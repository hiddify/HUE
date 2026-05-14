# HUE Documentation

Start here.

HUE is a multi-tenant control plane for VPN/proxy nodes. Phase 2
terminology: **Owner** (root, API key), **Reseller** (human admin tree,
JWT), **Client** (tenant that consumes traffic, JWT), **Agent**
(process on a Node, API key). See
[phase2-changes.md](phase2-changes.md) for the migration cheat-sheet
from phase-1 vocabulary.

## For users of the API

- **[How-to manual](manual.md)** — task-oriented walkthrough (bootstrap
  → reseller → client → node → agent → usage → ACME).
- **[API reference](api.md)** — services, RPCs, REST routes, error
  codes.
- **[Authentication](auth.md)** — API key + JWT model, Owner sudo,
  brute-force lockout, signing key rotation.

## For operators

- **[Deployment](deployment.md)** — Docker compose stack + Kubernetes
  manifests.
- **[Configuration](configuration.md)** — every `HUE_*` env var
  including `HUE_PASSWORD_ENC_KEY` (AES-256-GCM master key) and
  `HUE_ACME_*`.
- **[Operations](operations.md)** — health probes, observability,
  graceful shutdown, runbook.
- **[Certificates](certificates.md)** — shared domain cert store, BYO
  + ACME + self-signed fallback, AES-GCM at-rest.

## For contributors

- **[Architecture](architecture.md)** — layers, single-port handler,
  engine flow, reseller hierarchy enforcement.
- **[Development](development.md)** — local setup, regenerating proto
  + ent, running tests.
- **[Agents (protocol adapters)](agents.md)** — `pkg/agents/<proto>/`
  contract, template renderer, version selection rule, adding a new
  protocol.

## Reference

- [PRD.md](../PRD.md) — product requirements.
- [api/proto/hue/v1/hue.proto](../api/proto/hue/v1/hue.proto) — wire
  contract. **Source of truth for everything below it.**
- [phase2-changes.md](phase2-changes.md) — phase-1 → phase-2 mapping.
