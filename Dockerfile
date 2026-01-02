ARG BASEIMAGE=gcr.io/distroless/static-debian13:nonroot

FROM golang:1.25-trixie AS build

ARG TARGETOS
ARG TARGETARCH
ARG BUILDTARGET=rollout-operator

COPY . /src/rollout-operator
WORKDIR /src/rollout-operator
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} make ${BUILDTARGET}

FROM ${BASEIMAGE}

COPY --from=build /src/rollout-operator/rollout-operator /usr/bin/rollout-operator
ENTRYPOINT [ "/usr/bin/rollout-operator" ]

ARG revision
LABEL org.opencontainers.image.title="rollout-operator" \
      org.opencontainers.image.source="https://github.com/grafana/rollout-operator" \
      org.opencontainers.image.revision="${revision}"
