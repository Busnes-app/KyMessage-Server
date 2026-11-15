# Multi-stage build for kymessages

# Stage 1: Build React PWA Frontend
FROM node:26.13.1-alpine@sha256:6bd9ab43c0ec0190ca7b2d193906cb8b7edf6e7f4afa43ddeccd6452710bffb5 AS frontend-builder
WORKDIR /app/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Stage 2: Build Go Standalone Binary
FROM golang:1.27.4-alpine@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS backend-builder
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
FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
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
