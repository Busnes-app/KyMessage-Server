# Multi-stage build for kymessages

# Stage 1: Build React PWA Frontend
FROM node:26.12.1-alpine@sha256:40cd011b55415bc894e45b02fdd5a5dfde056921b7d23a64c74f80497af48797 AS frontend-builder
WORKDIR /app/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Stage 2: Build Go Standalone Binary
FROM golang:1.27.3-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS backend-builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=frontend-builder /app/web/dist ./web/dist
# Release builds pass the tag's version (X.Y.Z or X.Y.Z.N); other builds report internal/appversion/VERSION.
# -trimpath keeps the build directory out of the binary, so the same commit, Go version and
# VERSION give the same bytes wherever it is built (docs/VERIFYING-RELEASES.md).
ARG VERSION=
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -X github.com/Busnes-app/ky_server_base/internal/appversion.release=${VERSION}" \
    -o kymessages ./cmd/server

# Stage 3: Minimal Production Container
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
# postgresql17-client: pg_dump/pg_restore for Matrix backups; its major must equal the
# postgres service's (scripts/check-compose-matrix.sh).
RUN apk --no-cache add ca-certificates tzdata postgresql17-client
WORKDIR /app
COPY --from=backend-builder /app/kymessages /app/kymessages
# /app/backups is the optional mount for sealed local capsules; KY_BACKUP_DIR is set by the
# operator (compose does), so an image run bare keeps no local copies.
# Never root: Compose runs it as the owner of ./data (KY_UID, or KY_MATRIX_UID with Matrix);
# a bare run uses 10001, which owns the image's own volumes.
RUN mkdir -p /app/data /app/backups && chown 10001:10001 /app/data /app/backups
USER 10001:10001

ENV KY_PORT=8080
ENV KY_HOST=0.0.0.0
ENV KY_DATA_DIR=/app/data

EXPOSE 8080
VOLUME ["/app/data", "/app/backups"]

ENTRYPOINT ["/app/kymessages"]
