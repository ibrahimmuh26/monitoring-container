# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/monitoring-container ./cmd/monitoring-container
RUN mkdir -p /out/data && chmod 700 /out/data

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/monitoring-container /monitoring-container
COPY --from=build --chown=65532:65532 /out/data /var/lib/monitoring-container
USER 65532:65532
ENTRYPOINT ["/monitoring-container"]
CMD ["--config", "/etc/monitoring-container/config.yaml"]
