# SPDX-License-Identifier: MIT
# Home Assistant add-on Dockerfile (local-build FALLBACK path).
#
# PREFERRED PATH: do not build this file at all. Instead let the Supervisor
# pull the pre-built multi-arch image referenced by `image:` in
# addon/config.yaml (ghcr.io/sukramj/go-zendure2mqtt-addon-{arch}), produced by
# .github/workflows/addon-image.yml. See addon/README.md for the rationale.
#
# FALLBACK PATH (this file): a self-contained multi-stage build that compiles
# the Go binary itself and lands it on a Home Assistant base image so bashio is
# available to script/run.sh.
#
# IMPORTANT — BUILD CONTEXT ASSUMPTION:
#   The Go sources live at the REPOSITORY ROOT, not inside addon/. The builder
#   stage assumes the Docker build context is the repository root (so `COPY . .`
#   brings in go.mod, cmd/, internal/, and zendure.yaml). Build manually with
#   the repo root as context:
#
#     docker build \
#       --build-arg BUILD_FROM=ghcr.io/home-assistant/amd64-base:latest \
#       -f addon/Dockerfile -t go-zendure2mqtt-addon .
#
#   For Home Assistant installs, use the PREFERRED GHCR image path instead.

# BUILD_FROM (the per-arch HA base image) is consumed by the runtime stage's
# `FROM ${BUILD_FROM}`. An ARG used in a FROM must be declared before the first
# FROM to be a global build arg. The Supervisor (and addon-image.yml) supply it.
ARG BUILD_FROM

# ---------- Stage 1: build the Go binary ----------
FROM golang:1.26-alpine AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

# Repo-root build context: brings in cmd/, internal/, zendure.yaml.
COPY . .

ARG VERSION=addon
ARG COMMIT=none
ARG BUILD_DATE=unknown

ENV CGO_ENABLED=0
RUN go build -trimpath \
      -ldflags="-s -w \
        -X github.com/SukramJ/go-zendure2mqtt/internal/version.Version=${VERSION} \
        -X github.com/SukramJ/go-zendure2mqtt/internal/version.Commit=${COMMIT} \
        -X github.com/SukramJ/go-zendure2mqtt/internal/version.BuildDate=${BUILD_DATE}" \
      -o /out/zendure2mqtt ./cmd/zendure2mqtt

# ---------- Stage 2: Home Assistant add-on runtime ----------
ARG BUILD_FROM
FROM ${BUILD_FROM}

# The daemon resolves the catalog from its working directory (the --catalog
# default is the relative "zendure.yaml"), so place the file in /app and make
# /app the WORKDIR below.
COPY --from=builder /out/zendure2mqtt /usr/bin/zendure2mqtt
COPY --from=builder /src/zendure.yaml /app/zendure.yaml

WORKDIR /app

# Add-on entrypoint: reads /data/options.json and exec's the daemon.
COPY script/run.sh /run.sh
RUN chmod +x /run.sh

# Diagnostic web UI served through Ingress.
EXPOSE 8080

# Add-on image labels (BUILD_ARCH / BUILD_VERSION are injected by the
# Supervisor build; defaults keep manual builds from failing).
ARG BUILD_ARCH=amd64
ARG BUILD_VERSION=0.5.0
LABEL \
  io.hass.version="${BUILD_VERSION}" \
  io.hass.type="addon" \
  io.hass.arch="${BUILD_ARCH}"

CMD ["/run.sh"]
