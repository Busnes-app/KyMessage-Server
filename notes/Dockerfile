# KyMessages meeting-notes bot. Build context: notes/. glibc base: the LiveKit and ONNX
# runtimes ship glibc binaries. Models are fetched here at pinned revisions and checked
# (models.txt); the runtime image never downloads.
FROM node:26.12.1-slim@sha256:36793bcbb160a6a16cede1e62b93f84e26b78a972a8a4c0b879c1c12f22761ec AS build
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY package.json package-lock.json ./
# No install scripts: every native dependency ships its prebuilt binary in the package.
RUN npm ci --omit=dev --ignore-scripts --no-audit --no-fund
# ONNX Runtime ships every platform's binary; keep this one's.
ARG TARGETARCH
RUN arch=$([ "$TARGETARCH" = arm64 ] && echo arm64 || echo x64) && cd node_modules/onnxruntime-node/bin/napi-v3 && \
	rm -rf darwin win32 && find linux -mindepth 1 -maxdepth 1 ! -name "$arch" -exec rm -rf {} + && test -d "linux/$arch"
COPY models.txt fetch-models.sh ./
RUN sh fetch-models.sh models.txt /models >/dev/null
COPY src ./src

FROM node:26.12.1-slim@sha256:36793bcbb160a6a16cede1e62b93f84e26b78a972a8a4c0b879c1c12f22761ec
WORKDIR /app
COPY --from=build /app/package.json /app/
COPY --from=build /app/node_modules /app/node_modules
COPY --from=build /app/src /app/src
COPY --from=build /models /models
ENV NODE_ENV=production NOTES_MODELS_DIR=/models NOTES_DATA_DIR=/data
USER 10001:10001
CMD ["node", "src/main.mjs"]
