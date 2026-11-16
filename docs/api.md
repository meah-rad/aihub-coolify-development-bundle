# API Quick Reference

AIHub accepts source archives and delegates application deployment to Coolify after publishing an OCI image. The integration boundary and configuration requirements are documented in docs/coolify-integration.md.

All endpoints are under `/api/v1`. Browser sessions use an HttpOnly cookie and CSRF header. CLI calls use `Authorization: Bearer <token>`.

## Deploy

`POST /deployments` accepts `multipart/form-data` fields `source` (`.tar.gz`), `slug`, and optional `name`. Set an `Idempotency-Key` header. Returns `202` with deployment and service IDs.

`GET /deployments` lists the caller's deployments (admins see all). `GET /deployments/{id}` returns status and error details. `GET /deployments/{id}/events` streams saved deployment events as Server-Sent Events.

## Session and tokens

- `POST /auth/login`: JSON `{ "email": "...", "password": "..." }`.
- `POST /auth/logout`, `GET /auth/me`.
- `GET /tokens`, `POST /tokens` with JSON `{ "name": "AIHub Agent" }`, `DELETE /tokens/{id}`.
- `GET /users`, `POST /users` with `{ "email": "...", "password": "...", "role": "developer|viewer|admin" }`, and `PATCH /users/{id}` for role or disable status (admin only).

## Services and health

- `GET /services`, `GET /services/{id}`.
- `POST /services/{id}/actions` with `{ "action": "start|stop|restart|delete" }`.
- Service list responses include live CPU, memory, PID, and resource-availability fields when Docker stats are available. Admin overview includes a live host snapshot.
- `GET /healthz` is process liveness. `GET /readyz` checks database readiness.

## Coolify integration (admin only)

- `GET /admin/integrations/coolify` returns non-secret target settings and credential-configured flags.
- `PUT /admin/integrations/coolify` saves the Coolify URL/token, project/environment/server UUIDs, registry prefix, and registry push credentials. Empty secret fields preserve the existing secret.
- `POST /admin/integrations/coolify/test` checks API authentication and access to the selected project, environment, and server.

Errors use JSON with `code` and `message`; responses include `X-Request-ID`.
