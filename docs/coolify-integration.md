# Coolify Integration

## Product boundary

AIHub is the developer-facing source-package delivery portal. Coolify remains the product control plane and source of truth for applications, teams, deployment lifecycle, runtime configuration, domains, TLS, logs, rollback, host resources, and audit history.

AIHub reuses its React console, Go API and CLI, archive validation, Agent Skill, BuildKit templates, and build-event pipeline. It must not become a second runtime orchestrator: no direct production container management, edge routing, application lifecycle state machine, or duplicate host metrics.

## Deployment contract

Coolify's current API supports Docker Image applications, application updates, deploy actions, and deployment/status/log queries. It does not accept a local project archive as an application source. The source-package path therefore needs an image registry bridge:

1. The CLI uploads a filtered tar.gz archive to AIHub using its short-lived AIHub token and an idempotency key.
2. AIHub validates and extracts the archive, detects a supported project type, and builds with the existing BuildKit pipeline.
3. The worker pushes an immutable OCI image tag or digest to a registry that the target Coolify server can pull from.
4. The worker creates or updates a Coolify Docker Image application with the image reference, project/environment/server target, exposed port, and selected deployment settings.
5. The worker asks Coolify to deploy and records the Coolify application UUID, deployment UUID, image digest, and source revision against the AIHub build record.
6. The AIHub portal shows build output and the Coolify deployment result, with a deep link to the native Coolify application page.

Coolify owns runtime health checks, deploy ordering, cutover, rollback, retry, domains, runtime secrets, and start/stop/restart/delete. AIHub's job is complete only after it can reconcile a request with the Coolify deployment outcome; it must not mark a deployment successful merely because an image push succeeded.

## Required configuration

- Coolify base URL and server-side API token with only the required read, write, and deploy abilities.
- Default Coolify project UUID, environment identifier, and target server UUID, unless a later UI flow lets an administrator select among authorized targets.
- OCI registry hostname/namespace and robot credentials with push permissions. The target Coolify runtime must have pull access.
- Public or LAN URL conventions so the URL returned by Coolify is reachable by the intended users.

Store Coolify and registry credentials encrypted on the server. Mask them in settings responses and logs. Never pass them to the browser, CLI, project build context, or runtime container. Provide a connection check that verifies API connectivity and the configured target identifiers before enabling deployments.

## AIHub portal scope

The portal owns AI project-to-Coolify application mappings, source build records, build events, CLI tokens, and integration health. It links to Coolify for application settings, runtime logs, deployment history, domains, rollback, runtime actions, and host/resource views. Do not reproduce Coolify's general-purpose service management screens.

## Migration order

1. Completed: Coolify API client, encrypted integration settings, target connection check, and admin settings UI.
2. Completed: OCI registry publishing from the existing BuildKit worker using deployment-ID image tags and temporary Docker credentials outside the build context.
3. Completed: create/update Docker Image application, trigger deployment, and persist Coolify application/deployment UUIDs.
4. Completed: poll Coolify deployment state, record success/failure, and expose the Coolify application deep link in the services page.
5. Remaining: exercise the full flow against a configured Coolify instance and registry, then remove legacy Docker workload management and replace the prototype's duplicated host/runtime metrics.

## Current status

The source deployment worker now builds and pushes OCI images and delegates application deployment to Coolify. The Compose stack still contains its old Docker socket proxies and edge service for prototype compatibility and host metrics; those are not used to launch AIHub-managed workloads. No Coolify instance or registry credentials are configured in this workspace, so end-to-end deployment has not been exercised here.
