FROM golang:1.22-bookworm AS build

ARG TARGETOS
ARG TARGETARCH
ARG BUILDTARGET=rollout-operator

COPY . /src/rollout-operator
WORKDIR /src/rollout-operator
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} make ${BUILDTARGET}

FROM gcr.io/distroless/static-debian12

COPY --from=build /src/rollout-operator/rollout-operator /bin/rollout-operator
ENTRYPOINT [ "/bin/rollout-operator" ]

# Create rollout-operator user to run as non-root.
RUN addgroup -g 10000 -S rollout-operator && \
    adduser  -u 10000 -S rollout-operator -G rollout-operator
USER rollout-operator:rollout-operator

ARG revision
LABEL org.opencontainers.image.title="rollout-operator" \
      org.opencontainers.image.source="https://github.com/grafana/rollout-operator" \
      org.opencontainers.image.revision="${revision}"
