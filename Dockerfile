# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.26-trixie AS gobuilder
WORKDIR /app
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=bind,target=/app \
    CGO_ENABLED=0 GOARCH=$TARGETARCH go build -ldflags="-s" -trimpath -o /bin/iptv . && \
    CGO_ENABLED=0 GOARCH=$TARGETARCH go build -ldflags="-s" -trimpath -o /bin/healthcheck ./healthcheck

FROM --platform=$BUILDPLATFORM debian:trixie-20260421 AS tsreadexbuilder
RUN apt-get update && apt-get install -y --no-install-recommends \
    g++ make git ca-certificates
RUN git clone https://github.com/xtne6f/tsreadex /tsreadex
RUN make -C /tsreadex LDFLAGS=-static

FROM gcr.io/distroless/static-debian13
COPY --from=gobuilder /bin/iptv /usr/local/bin/iptv
COPY --from=gobuilder /bin/healthcheck /usr/local/bin/healthcheck
COPY --from=tsreadexbuilder /tsreadex/tsreadex /usr/local/bin/tsreadex
EXPOSE 8080
CMD ["/usr/local/bin/iptv"]
