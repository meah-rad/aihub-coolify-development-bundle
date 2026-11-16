---
name: aihub-deploy
description: Package and deploy the current Node.js or Python web project to the company AI Hub Cloud platform, then report its build result and internal URL.
---

# AI Hub Deploy

Use this skill when the user asks to publish or host the current project on the internal AI Hub Cloud platform.

## Before deploying

1. Inspect the project root and identify whether it is a supported Node.js app (`package.json` with a `start` script) or a Python app (`requirements.txt` and an explicit web command).
2. Add or update `platform.yaml` with the service slug, HTTP port, command when it cannot be safely inferred, and health path. Never put secrets in this file.
3. Run the project checks available in the repository. Do not run the deployment if tests or the production build are failing unless the user explicitly asks to deploy anyway.
4. Check that `.env`, credentials, private keys, generated dependencies, and local data are excluded from the upload.
5. If the app needs secrets, deploy the project first, then configure them in the service's **环境变量** editor. Values are write-only in the console. Redeploy after changing them; never paste their values into chat or build logs.

## Deploy

Run from the project root:

```sh
aihubctl deploy --slug <short-service-name> --name "<Display Name>" --dir . --url <internal-platform-url>
```

The CLI reads `AIHUB_TOKEN` from the environment. Never print or include the token in chat, command output, source archives, or logs. If the CLI is not installed, ask an administrator to install the platform CLI; do not reproduce the token in a shell command.

Report the deployment ID and status. Open the internal URL only after the deployment reports healthy. On failure, read the deployment events and return the actionable build error without exposing secrets.

## Project contract

`platform.yaml` example:

```yaml
name: demo-site
web:
  port: 3000
  command: npm start
  healthPath: /
```

For Python, set `web.command` explicitly, for example `python app.py`. The platform generates a constrained runtime image; user Dockerfiles and Compose files are not accepted in v1.
