FROM swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/library/node:24-alpine AS frontend
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/library/golang:1.25-alpine AS backend
WORKDIR /src/backend
RUN apk add --no-cache git
ARG GOPROXY=https://proxy.golang.org,direct
COPY backend/go.mod ./go.mod
RUN GOPROXY=${GOPROXY} go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/aihub ./cmd/aihub && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/aihubctl ./cmd/aihubctl

FROM swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/moby/buildkit:rootless AS buildkit-tools

FROM swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/library/alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 aihub
WORKDIR /app
COPY --from=backend /out/aihub /usr/local/bin/aihub
COPY --from=backend /out/aihubctl /usr/local/bin/aihubctl
COPY --from=buildkit-tools /usr/bin/buildctl /usr/local/bin/buildctl
COPY --from=frontend /src/frontend/dist /app/static
RUN mkdir -p /var/lib/aihub/data /var/lib/aihub/routes && chown -R aihub:aihub /var/lib/aihub
USER 10001
ENV DATA_ROOT=/var/lib/aihub/data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/aihub"]
