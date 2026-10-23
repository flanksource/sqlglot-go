FROM gcr.io/distroless/static-debian12:nonroot
ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/sqlglot /usr/local/bin/sqlglot
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/sqlglot"]
