FROM gcr.io/distroless/static-debian13:nonroot

COPY       mock-service /usr/bin/mock-service
ENTRYPOINT [ "/usr/bin/mock-service" ]

ARG revision
LABEL org.opencontainers.image.title="mock-service" \
      org.opencontainers.image.source="https://github.com/grafana/rollout-operator/integration/mock-service" \
      org.opencontainers.image.revision="${revision}"
