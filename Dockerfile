FROM --platform=$BUILDPLATFORM golang:1.27-trixie@sha256:c65ecfe613b539ac74c62a4c3b2c27e8f8a6076384c8f27e8a3c0f3be17cc385 AS build

ARG TARGETOS
ARG TARGETARCH

COPY . /src/rollout-operator
WORKDIR /src/rollout-operator
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} make rollout-operator

FROM gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329

COPY --from=build /src/rollout-operator/rollout-operator /usr/bin/rollout-operator
ENTRYPOINT [ "/usr/bin/rollout-operator" ]

ARG revision
LABEL org.opencontainers.image.title="rollout-operator" \
      org.opencontainers.image.source="https://github.com/grafana/rollout-operator" \
      org.opencontainers.image.revision="${revision}"
