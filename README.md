# AIHub Cloud

AIHub Cloud is a source-package delivery portal for deploying web projects through [Coolify](https://coolify.io/). It includes a React administration console, a Go API and CLI, source archive validation, an Agent Skill, and a BuildKit-based image build pipeline.

AIHub validates and builds supported project archives, pushes immutable OCI images to a registry, and asks Coolify to deploy them. Coolify remains the control plane for application runtime, domains, TLS, logs, rollback, and host resources. The included Coolify source tree is for development and inspection; this Compose stack does not install Coolify for you.

## Components

- `backend/`: Go API, worker, database migrations, and `aihubctl` CLI.
- `frontend/`: React and Vite administration console.
- `skills/aihub-deploy/`: Agent Skill for preparing and deploying projects.
- `samples/`: minimal Node.js and Python web applications.
- `coolify/coolify-main/`: included Coolify source tree and its upstream license and notices.
- `docs/`: API reference, integration design, development plan, and Ubuntu deployment notes.

## Quick Start

Requirements: Docker Engine with the Compose plugin. Go 1.25 and Node.js 24 are needed for local development and tests; Docker builds use these versions in their build stages.

1. Create a local environment file and set unique secrets:

   ```bash
   cp .env.example .env
   ```

   Set strong `POSTGRES_PASSWORD` and `ADMIN_PASSWORD` values. Generate `APP_ENCRYPTION_KEY` with `openssl rand -base64 32`. Do not commit `.env`.

2. Validate and start the stack:

   ```bash
   docker compose config --quiet
   docker compose up -d --build
   docker compose ps
   ```

3. Open `http://localhost:12688` (or the port configured by `EDGE_PORT`) and sign in with the administrator credentials from `.env`.

4. In **平台设置 → Coolify 集成**, configure the Coolify URL and API token, project/environment/server identifiers, and an OCI registry with push credentials. Ensure the Coolify runtime can pull from that registry, then use **测试连接**.

5. Create an AIHub deployment token and deploy a sample from a machine with the CLI:

   ```bash
   cd backend
   go build -o aihubctl ./cmd/aihubctl
   export AIHUB_TOKEN='<deployment-token>'
   ./aihubctl deploy --slug node-demo --dir ../samples/node-web --url http://localhost:12688
   ```

The Compose stack uses image names mirrored through Huawei Cloud. If the target Linux host cannot access that mirror, retag approved local image copies or update the image references before building. See [Ubuntu deployment notes](docs/ubuntu-24.04-handoff.md) for host setup and troubleshooting.

## Development

Run the Go tests and frontend build:

```bash
cd backend && go test ./...
cd ../frontend && npm ci && npm test
```

Other useful commands:

```bash
make test
make build
docker compose logs -f api worker
```

See [API quick reference](docs/api.md) and [Coolify integration](docs/coolify-integration.md) for endpoint details, deployment flow, and known gaps.

## Security Notes

- The archive intentionally excludes `.env`, runtime data, dependency caches, and prebuilt images. `.env.example` contains placeholders only.
- Keep the AIHub management network private. The BuildKit daemon uses an internal plaintext TCP endpoint and its Compose service runs privileged for rootless BuildKit compatibility.
- This bundle is intended for trusted development/LAN use. Configure TLS and firewall rules before exposing the portal, and do not accept untrusted public workloads until build isolation is hardened.
- Back up `APP_ENCRYPTION_KEY` securely. Losing it makes stored Coolify and registry credentials undecryptable.
- A real Coolify/registry deployment requires separately configured services and credentials; follow the handoff guide before using a production host.
- Hard-coded Coolify development/test private-key fixtures from the source bundle have been replaced with placeholders for GitHub secret protection. Coolify tests or seeders that require SSH keys need locally generated test keys; the development GitHub App seeder reads `COOLIFY_DEVELOPMENT_GITHUB_APP_PRIVATE_KEY` from the local environment.

## Licensing

AIHub-specific files are licensed under the MIT License in the repository root. The included Coolify source tree retains its upstream Apache-2.0 `LICENSE` and notices; that license applies to Coolify, not to the AIHub-specific files. Other bundled third-party components may carry their own licenses and notices.
