FROM golang:1.20-alpine3.18 AS build

ARG TARGETOS
ARG TARGETARCH
ARG BUILDTARGET=rollout-operator

RUN apk add --no-cache build-base git

COPY . /src/rollout-operator
WORKDIR /src/rollout-operator
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} make ${BUILDTARGET}

FROM alpine:3.18
RUN apk add --no-cache ca-certificates

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
