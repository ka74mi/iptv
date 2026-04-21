FROM golang:1.26-bookworm AS gobuilder
WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 go build -o iptv .

FROM debian:bookworm AS tsreadexbuilder
RUN apt-get update && apt-get install -y --no-install-recommends \
    g++ make git ca-certificates
RUN git clone https://github.com/xtne6f/tsreadex /tsreadex
RUN make -C /tsreadex LDFLAGS=-static

FROM gcr.io/distroless/static-debian13
COPY --from=gobuilder /app/iptv /usr/local/bin/iptv
COPY --from=tsreadexbuilder /tsreadex/tsreadex /usr/local/bin/tsreadex
EXPOSE 8080
CMD ["/usr/local/bin/iptv"]
