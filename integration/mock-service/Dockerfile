FROM gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329

COPY       mock-service /usr/bin/mock-service
ENTRYPOINT [ "/usr/bin/mock-service" ]

ARG revision
LABEL org.opencontainers.image.title="mock-service" \
      org.opencontainers.image.source="https://github.com/grafana/rollout-operator/integration/mock-service" \
      org.opencontainers.image.revision="${revision}"
