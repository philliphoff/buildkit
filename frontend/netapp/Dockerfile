# syntax = docker/dockerfile:1.1-experimental

FROM --platform=$BUILDPLATFORM tonistiigi/xx:golang@sha256:6f7d999551dd471b58f70716754290495690efa8421e0a1fcf18eb11d0c0a537 AS xgo

FROM --platform=$BUILDPLATFORM golang:1.13-buster AS base
COPY --from=xgo / /
WORKDIR /src
ENV GOFLAGS=-mod=vendor

FROM base AS build
RUN apt-get update && apt-get --no-install-recommends install -y file
ARG BUILDTAGS=""
ARG TARGETPLATFORM
ENV TARGETPLATFORM=$TARGETPLATFORM
RUN --mount=target=. --mount=type=cache,target=/root/.cache \
  --mount=target=/go/pkg/mod,type=cache \
  CGO_ENABLED=0 go build -o /netapp-frontend -ldflags "-d $(cat /tmp/.ldflags)" -tags "$BUILDTAGS netgo static_build osusergo" ./frontend/netapp && \
  file /netapp-frontend | grep "statically linked"

FROM scratch AS release
LABEL moby.buildkit.frontend.network.none="true"
LABEL moby.buildkit.frontend.caps="moby.buildkit.frontend.inputs,moby.buildkit.frontend.subrequests"
COPY --from=build /netapp-frontend /bin/netapp-frontend
ENTRYPOINT ["/bin/netapp-frontend"]

FROM release
