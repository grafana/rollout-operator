FROM alpine:3.18

COPY       mock-service /bin/mock-service
ENTRYPOINT [ "/bin/mock-service" ]

# Create mock-service user to run as non-root.
RUN addgroup -g 10000 -S mock-service && \
    adduser  -u 10000 -S mock-service -G mock-service
USER mock-service:mock-service

ARG revision
LABEL org.opencontainers.image.title="mock-service" \
      org.opencontainers.image.source="https://github.com/grafana/rollout-operator/integration/mock-service" \
      org.opencontainers.image.revision="${revision}"
