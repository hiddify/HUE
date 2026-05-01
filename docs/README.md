# HUE Documentation

Start here.

## For users of the API

- **[How-to manual](manual.md)** — task-oriented walkthrough: bootstrap → tenant model → onboard nodes → provision users → report usage → daily ops. Start here if you've just deployed HUE.
- **[API reference](api.md)** — services, RPCs, REST routes, examples.
- **[Authentication](auth.md)** — how to mint and use an API key.

## For operators

- **[Deployment](deployment.md)** — Docker compose stacks and Kubernetes manifests.
- **[Configuration](configuration.md)** — every `HUE_*` env var.
- **[Operations](operations.md)** — health probes, observability, graceful shutdown, troubleshooting.

## For contributors

- **[Architecture](architecture.md)** — components, data flow, the single-port handler, why every design decision was made.
- **[Development](development.md)** — local setup, regenerating proto + ent, running tests, fixing common gotchas.

## Reference material elsewhere

- [PRD.md](../PRD.md) — product requirements (the *what* and *why*).
- [REVIEW.md](../REVIEW.md) — audit of the previous implementation. Lists the regressions that the rewrite specifically fixes; useful when reading the code's `// REVIEW.md ...` comments.
- [api/proto/hue/v1/hue.proto](../api/proto/hue/v1/hue.proto) — wire contract. Source of truth for everything below it.
