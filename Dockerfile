FROM --platform=$BUILDPLATFORM golang:1.26-trixie AS build

ARG TARGETOS
ARG TARGETARCH

COPY . /src/rollout-operator
WORKDIR /src/rollout-operator
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} make rollout-operator

FROM gcr.io/distroless/static-debian13:nonroot

COPY --from=build /src/rollout-operator/rollout-operator /usr/bin/rollout-operator
ENTRYPOINT [ "/usr/bin/rollout-operator" ]

ARG revision
LABEL org.opencontainers.image.title="rollout-operator" \
      org.opencontainers.image.source="https://github.com/grafana/rollout-operator" \
      org.opencontainers.image.revision="${revision}"
