# syntax=docker/dockerfile:1
FROM golang:1.26.9@sha256:37cfcca63222ec499bec5e8ad7aad52dbf7529db36d5812b8eae2f0621aa92d6 AS build
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
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/hhc-query /hhc-query
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/hhc-query"]
CMD ["--listen", "0.0.0.0:8080"]
