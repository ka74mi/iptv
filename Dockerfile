FROM golang:1.26-bookworm AS gobuilder
WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 go build -o iptv .

FROM gcr.io/distroless/static-debian13
COPY --from=gobuilder /app/iptv /usr/local/bin/iptv
EXPOSE 8080
CMD ["/usr/local/bin/iptv"]
