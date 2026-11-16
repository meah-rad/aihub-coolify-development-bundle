# Ubuntu 24.04 Deployment Handoff

This bundle contains the AIHub source-package portal/deployment bridge and a clean source copy of the pulled Coolify project. It does not contain `.env` credentials, Coolify runtime data, dependency caches, or prebuilt images. The AIHub portal image is built locally with Docker Compose. Coolify is installed and run separately; the included Coolify source tree is for inspection and source development.

## Target host notes

The supplied host check reports Ubuntu 24.04.4, kernel 6.8, Docker Engine 29.4.0, cgroup v2, overlayfs, 16 CPUs, 46 GiB RAM, and approximately 282 GiB free on `/`. The privileged-container probe passed. This is sufficient for a single-node Coolify plus AIHub development setup, with capacity headroom to monitor during concurrent image builds.

The ports checked by the supplied command (80, 443, 8088, and 5000) were free at handoff time. Before installing Coolify, also check its direct UI and auxiliary ports (8000, 6001, and 6002), and reserve 12688 for the AIHub portal:

```bash
sudo ss -lntp | grep -E ':(80|443|8000|6001|6002|12688)\b' || echo 'coolify-and-aihub-ports-free'
```

Do not expose the Coolify admin UI/API ports to the public Internet. Restrict its UI, WebSocket, and terminal access to a trusted LAN/VPN or administrator IPs; expose application ports 80/443 only as required by your deployment. The AIHub portal on 12688 should also be firewall-restricted until HTTPS is configured.

`curl https://swr.cn-north-4.myhuaweicloud.com/v2/` returning HTTP 401 is normally an OCI registry authentication challenge, not proof that the registry is unreachable. Confirm image access by pulling an actual image. If the real image pull is denied or times out, use the registry/network fallback below.

## 1. Install Coolify

Install Coolify before configuring AIHub. Follow the current [official self-hosted installation guide](https://coolify.io/docs/start-with-self-hosted). It recommends a fresh server where possible; this machine already has a working Docker Engine, so inspect the installer and check for a Docker Snap installation before running it. The official guide provides a manual installation path for existing Docker and custom setups. Do not remove existing Docker data or volumes as a troubleshooting step. Coolify's first registration can grant server-admin access, so preconfigure its root account rather than leaving registration open on a network-reachable port.

```bash
sudo -i
snap list docker 2>/dev/null || true
curl -fsSL https://cdn.coollabs.io/coolify/install.sh -o /tmp/coolify-install.sh
less /tmp/coolify-install.sh
read -rp 'Coolify root username: ' ROOT_USERNAME
read -rp 'Coolify root email: ' ROOT_USER_EMAIL
read -rsp 'Coolify root password: ' ROOT_USER_PASSWORD; echo
export ROOT_USERNAME ROOT_USER_EMAIL ROOT_USER_PASSWORD
bash /tmp/coolify-install.sh
unset ROOT_USERNAME ROOT_USER_EMAIL ROOT_USER_PASSWORD
```

Create the Coolify administrator account promptly and finish its server/proxy setup. Keep ports 80/443 reserved for Coolify-managed application traffic. Restrict direct ports 8000/6001/6002 to trusted administrator networks; after the dashboard is verified through the Coolify proxy/domain, close direct public access to them. Confirm the Coolify UI and API are reachable from the host. If Coolify listens on host port 8000, AIHub containers can use `http://host.docker.internal:8000`; the Compose file includes the Linux `host-gateway` mapping. Prefer an HTTPS Coolify domain when one is available.

In Coolify, prepare:

- A project and environment for AIHub deployments.
- A deployment server UUID.
- A private OCI registry/repository namespace the host can push to and Coolify can pull from. GHCR, Harbor, or another reachable registry can be used; create separate minimum-scope push and pull credentials where possible.
- A Coolify API token scoped to the team that owns the project and with read, write, deploy, and delete abilities. Delete is needed only for the portal's application-delete action.

Record the Coolify project, environment, and server UUIDs. Ensure the registry is configured in Coolify for image pulls; the credentials entered in AIHub are the build worker's push credentials and do not automatically configure Coolify's pull credentials.

## 2. Unpack the AIHub bundle

Copy the archive to the host, verify its SHA-256 using the value supplied with the transfer, then unpack it under `/opt`:

```bash
sudo install -d -m 0755 /opt/aihub
sudo tar -xzf aihub-coolify-development-bundle-2026-11-11.tar.gz -C /opt/aihub
cd /opt/aihub
```

The archive deliberately excludes `.env` credentials, Coolify environment files and runtime storage, `node_modules`, `vendor`, built frontend assets, and dependency/build caches. The Coolify source is under `coolify/coolify-main`; it is not built into the AIHub portal image and is not a running Coolify installation.

## 3. Configure AIHub

Create a deployment-specific environment file. Use strong, distinct random values for the database and administrator passwords. The API safely URL-escapes the database password when constructing its PostgreSQL connection string, so it does not need to be restricted to hexadecimal characters.

```bash
cd /opt/aihub
cp .env.example .env
openssl rand -hex 24 # POSTGRES_PASSWORD
openssl rand -hex 24 # ADMIN_PASSWORD
openssl rand -base64 32 # APP_ENCRYPTION_KEY
sudo chmod 600 .env
sudoedit .env
```

Set the first hex value as `POSTGRES_PASSWORD`, the second as a distinct `ADMIN_PASSWORD`, and the base64 value as `APP_ENCRYPTION_KEY`. Set:

- `PLATFORM_HOST` to the DNS name or IP address used in the browser URL. For example, `aihub.example.internal` or the server's LAN IP.
- `EDGE_PORT=12688` unless you intentionally choose another unused port.
- `COOKIE_SECURE=true` only when the browser reaches the portal through HTTPS. The included edge proxy is HTTP-only; place trusted TLS termination in front of it before setting this.

Build the stack and check service health. If `proxy.golang.org` is unreachable from Docker build containers, pass a reachable Go module proxy through the build argument shown here:

```bash
docker compose config --quiet
docker compose build --build-arg GOPROXY=https://goproxy.cn,direct
docker compose up -d --no-build --pull never
docker compose ps
docker compose logs --tail=100 api worker buildkit edge
curl -i -H 'Host: aihub.example.internal' http://127.0.0.1:12688/healthz
```

Replace the example Host header with the value of `PLATFORM_HOST`. Open `http://<PLATFORM_HOST>:12688` and sign in using the configured `ADMIN_EMAIL` and `ADMIN_PASSWORD`.

## 4. Connect AIHub to Coolify

In the AIHub console, open **平台设置 → Coolify 集成** and enter:

- Coolify URL: `http://host.docker.internal:8000` for a same-host Coolify on port 8000, or its actual HTTPS URL.
- Coolify API token.
- Project UUID, Environment UUID, and Server UUID.
- Registry prefix in `registry.example.com/namespace/repository` form. Do not include `https://` or a tag.
- Registry push username and password/token.

Save, then use **测试连接**. The check verifies API access to the selected project, environment, and server. Fix token abilities or UUIDs if it fails. The source build pushes tags shaped as `<registry-prefix>/<slug>:<deployment-id>`. The worker generates a unique immutable tag for every deployment.

## 5. Deploy a sample

Create an AIHub deployment token in **访问令牌**. Build the CLI on a workstation with Go 1.25, or build it on the server:

```bash
mkdir -p "$HOME/.local/bin"
cd /opt/aihub/backend
go build -o "$HOME/.local/bin/aihubctl" ./cmd/aihubctl
```

If Go 1.25 is not installed on the server, build a Linux binary inside Docker instead:

```bash
cd /opt/aihub/backend
docker run --rm -v "$PWD:/src" -w /src golang:1.25-alpine go build -o aihubctl ./cmd/aihubctl
chmod 700 ./aihubctl
```

On the server, use `/opt/aihub/samples/node-web` as the `--dir`. On a workstation, replace it with the local project directory and build the CLI for Linux/amd64 if the workstation is Windows. Set `AIHUB_TOKEN` securely and deploy:

```bash
export AIHUB_TOKEN='<AIHub deployment token>'
aihubctl deploy --slug node-demo --dir /opt/aihub/samples/node-web --url http://<PLATFORM_HOST>:12688
```

For deployment from a workstation, `--dir` is the local sample/project directory. The CLI waits for completion by default. Then inspect:

```bash
aihubctl status --id <deployment-id> --url http://<PLATFORM_HOST>:12688
aihubctl logs --id <deployment-id> --url http://<PLATFORM_HOST>:12688
```

The Services page shows both the public application URL and a link to its Coolify application. Runtime configuration, domains, logs, rollback, and host resources remain managed in Coolify.

## Troubleshooting

### Registry access

An unauthenticated `/v2/` response of 401 is expected for many registries. Test a real pull from the host and verify the exact registry/repository path. If Huawei mirror pulls fail, use approved public image names for the base images in `Dockerfile`, `docker-compose.yml`, and the generated Node/Python Dockerfiles in `backend/internal/worker/worker.go`, or configure the required mirror credentials. Rebuild with `docker compose up -d --build` after changing image sources.

### Check worker logs and queue state

```bash
docker compose logs -f worker
docker compose logs -f buildkit
docker compose logs -f api
```

The worker records archive validation, build/push failures, Coolify API errors, Coolify deployment UUIDs, and terminal deployment results as AIHub deployment events. A deployment is not marked successful just because the image push succeeded.

### Update source and rebuild

Back up `.env` and the Compose volumes before replacing a running checkout. Rebuild the portal/worker after source changes:

```bash
docker compose up -d --build api worker
docker compose logs --tail=100 api worker
```

`docker compose down` preserves named volumes. Do not run `docker compose down -v` unless you intend to delete the database, uploaded build artifacts, and BuildKit cache.

## Known limitations

- This bundle includes the AIHub portal and bridge, not the Coolify application itself.
- A real Coolify/registry end-to-end deployment was not run in the packaging environment; the API, Go tests, frontend build, Compose validation, and production Docker build were verified.
- The Compose file retains Docker socket proxies and the edge proxy for prototype compatibility and host snapshots. AIHub workload deployments now go through Coolify, not those direct Docker workload APIs.
- BuildKit currently runs privileged and listens on the private Compose network over plaintext TCP. Do not expose that network or accept untrusted public workloads until the build isolation model is hardened.
- Build output is stored after the build process exits rather than streamed line-by-line. After a worker restart, deployment jobs left running for more than 45 minutes are requeued; if the Coolify deployment UUID was persisted, the worker resumes polling it. A crash after Coolify accepts a start request but before its UUID is stored can still result in a duplicate trigger.
- Configure TLS and firewall policy before exposing the portal beyond a trusted development LAN.
