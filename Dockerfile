ARG BASEIMAGE=gcr.io/distroless/static-debian12:nonroot

FROM golang:1.23-bookworm AS build

ARG TARGETOS
ARG TARGETARCH
ARG BUILDTARGET=rollout-operator

COPY . /src/rollout-operator
WORKDIR /src/rollout-operator
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} make ${BUILDTARGET}

FROM ${BASEIMAGE}

COPY --from=build /src/rollout-operator/rollout-operator /bin/rollout-operator
ENTRYPOINT [ "/bin/rollout-operator" ]

ARG revision
LABEL org.opencontainers.image.title="rollout-operator" \
      org.opencontainers.image.source="https://github.com/grafana/rollout-operator" \
      org.opencontainers.image.revision="${revision}"
