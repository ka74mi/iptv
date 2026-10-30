# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27-trixie AS gobuilder
WORKDIR /app
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=bind,target=/app \
    CGO_ENABLED=0 GOARCH=$TARGETARCH go build -ldflags="-s" -trimpath -o /bin/iptv . && \
    CGO_ENABLED=0 GOARCH=$TARGETARCH go build -ldflags="-s" -trimpath -o /bin/healthcheck ./healthcheck

# tsreadex はネイティブバイナリなので、--platform=$BUILDPLATFORM を付けずターゲットの
# アーキテクチャでビルドする（付けるとクロスビルド時に別アーキのバイナリが入る）。
FROM debian:13 AS tsreadexbuilder
RUN apt-get update && apt-get install -y --no-install-recommends \
    g++ make git ca-certificates
# 再現性とサプライチェーン対策のため、ブランチやタグではなくコミットSHAで固定する。
# （タグは付け替えられるが、SHAは内容が変わると別の値になる）
# 現在の値は upstream のタグ master-260428 のコミット。更新するときは、
# 新しいタグのコミットSHA（40桁）に書き換えるか、--build-arg TSREADEX_COMMIT=... で指定する。
ARG TSREADEX_COMMIT=a82528ccb698fcd07b4da1bb2243e63d685c34a7
RUN git init -q /tsreadex && cd /tsreadex && \
    git fetch -q --depth 1 https://github.com/xtne6f/tsreadex "${TSREADEX_COMMIT}" && \
    git checkout -q --detach FETCH_HEAD && \
    test "$(git rev-parse HEAD)" = "${TSREADEX_COMMIT}"
RUN make -C /tsreadex LDFLAGS=-static

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=gobuilder /bin/iptv /usr/local/bin/iptv
COPY --from=gobuilder /bin/healthcheck /usr/local/bin/healthcheck
COPY --from=tsreadexbuilder /tsreadex/tsreadex /usr/local/bin/tsreadex
USER 65532:65532
# 既定の待ち受けポート。LISTEN_ADDR を変えても、この行は情報（ドキュメント）としてのみ使われる。
EXPOSE 8080
CMD ["/usr/local/bin/iptv"]
