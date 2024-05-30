FROM gcr.io/distroless/static-debian12:nonroot

COPY       mock-service /bin/mock-service
ENTRYPOINT [ "/bin/mock-service" ]

ARG revision
LABEL org.opencontainers.image.title="mock-service" \
      org.opencontainers.image.source="https://github.com/grafana/rollout-operator/integration/mock-service" \
      org.opencontainers.image.revision="${revision}"
