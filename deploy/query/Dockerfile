# syntax=docker/dockerfile:1
FROM golang:1.26.10@sha256:670bb5aa5349ae03b8516764718486f4f16399134bdc9acd0c46a23f043ab05c AS build
WORKDIR /src
COPY go.mod go.sum ./
# The optional CA secret supports managed build proxies without baking their
# session trust into the image. Normal builds use the image's CA trust.
RUN --mount=type=secret,id=build_ca \
    if [ -f /run/secrets/build_ca ]; then SSL_CERT_FILE=/run/secrets/build_ca go mod download; else go mod download; fi
COPY internal ./internal
COPY cmd/hhc-query ./cmd/hhc-query
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/hhc-query ./cmd/hhc-query
RUN mkdir -p /out/licenses && cp /usr/local/go/LICENSE /out/licenses/Go-LICENSE && \
    go list -deps -f '{{with .Module}}{{.Path}}@{{.Version}}|{{.Dir}}{{end}}' ./cmd/hhc-query | sort -u | \
    while IFS='|' read -r module_name module_dir; do \
      if [ -n "$module_dir" ] && [ -f "$module_dir/LICENSE" ]; then \
        printf '\n%s\n' "$module_name" >> /out/licenses/dependencies.txt; \
        cat "$module_dir/LICENSE" >> /out/licenses/dependencies.txt; \
      fi; \
    done
FROM scratch
ARG VERSION=dev
LABEL org.opencontainers.image.revision=$VERSION
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/hhc-query /hhc-query
COPY --from=build /out/licenses /licenses
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/hhc-query"]
CMD ["--listen", "0.0.0.0:8080"]
