FROM --platform=$BUILDPLATFORM golang:1.27-trixie AS build

ARG TARGETOS
ARG TARGETARCH

COPY . /src/rollout-operator
WORKDIR /src/rollout-operator
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} make rollout-operator

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99

COPY --from=build /src/rollout-operator/rollout-operator /usr/bin/rollout-operator
ENTRYPOINT [ "/usr/bin/rollout-operator" ]

ARG revision
LABEL org.opencontainers.image.title="rollout-operator" \
      org.opencontainers.image.source="https://github.com/grafana/rollout-operator" \
      org.opencontainers.image.revision="${revision}"
