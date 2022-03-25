VERSION 0.6

ko:
    FROM alpine:3.15.0

    RUN apk add curl tar gzip

    ARG VERSION=0.11.2
    ARG OS=Linux
    ARG ARCH=x86_64

    WORKDIR /working

    RUN curl -L -o ko.tar.gz https://github.com/google/ko/releases/download/v${VERSION}/ko_${VERSION}_${OS}_${ARCH}.tar.gz && \
            tar xvfz ko.tar.gz && \
            chmod +x ./ko

    SAVE ARTIFACT ko

setup:
    FROM goreleaser/goreleaser:v1.6.3

    WORKDIR /workspace
    COPY go.mod go.mod
    COPY go.sum go.sum
    RUN go mod download

    COPY cmd/ cmd/
    COPY controllers/ controllers/
    COPY internal/ internal/
    COPY config/ config/

    COPY +busybox-linux-amd64/busybox internal/k8s/offline/payload/busybox-linux-amd64
    COPY +busybox-linux-arm64/busybox internal/k8s/offline/payload/busybox-linux-arm64
    COPY +busybox-linux-arm-v6/busybox internal/k8s/offline/payload/busybox-linux-arm-v6
    COPY +busybox-linux-arm-v7/busybox internal/k8s/offline/payload/busybox-linux-arm-v7

build-images:
    FROM +setup

    ARG GGCR_EXPERIMENT_ESTARGZ=1

    COPY +ko/ko .

    RUN ./ko build ./cmd/ripfs --push=false --sbom spdx --platform linux/amd64,linux/arm64,linux/arm/v6,linux/arm/v7 --oci-layout-path oci
    SAVE ARTIFACT oci

publish-images:
    FROM +setup

    ARG GGCR_EXPERIMENT_ESTARGZ=1
    ARG KO_DOCKER_REPO=ghcr.io/joshrwolf/ripfs

    COPY +ko/ko .

    RUN ./ko build ./cmd/ripfs --sbom spdx --platform linux/amd64,linux/arm64,linux/arm/v6,linux/arm/v7

build-bin:
    FROM +setup

    COPY .goreleaser.yaml .goreleaser.yaml
    RUN goreleaser build --rm-dist --snapshot

publish-bin:
    FROM +setup

    COPY .goreleaser.yaml .goreleaser.yaml
    RUN goreleaser build --rm-dist

publish
    BUILD +publish-images
    BUILD +publish-bin

busybox:
    FROM alpine:3.15.0

    ARG BUSYBOX_VERSION=1.34.1
    ARG BUSYBOX_TARBALL_URL=https://busybox.net/downloads/busybox-${BUSYBOX_VERSION}.tar.bz2
    ARG BUSYBOX_TARBALL_CHECKSUM=415fbd89e5344c96acf449d94a6f956dbed62e18e835fc83e064db33a34bd549

    RUN uname -m

    RUN apk add --no-cache \
            build-base \
            ca-certificates \
            curl \
            lz4-dev \
            lz4-static \
            openssl-dev \
            openssl-libs-static \
            perl \
            zlib-dev \
            zlib-static \
            zstd-dev \
            zstd-static

    RUN mkdir /tmp/busybox/
    WORKDIR /tmp/busybox/
    RUN curl -Lo /tmp/busybox.tbz2 "${BUSYBOX_TARBALL_URL:?}"

    RUN printf '%s' "${BUSYBOX_TARBALL_CHECKSUM:?}  /tmp/busybox.tbz2" | sha256sum -c
    RUN tar -xjf /tmp/busybox.tbz2 --strip-components=1 -C /tmp/busybox/
    RUN make allnoconfig

    RUN setcfg() { sed -ri "s/^(# )?(${1:?})( is not set|=.*)$/\2=${2?}/" ./.config; } \
        && setcfg CONFIG_STATIC                y \
        && setcfg CONFIG_LFS                   y \
        && setcfg CONFIG_BUSYBOX               y \
        && setcfg CONFIG_FEATURE_SH_STANDALONE y \
        && setcfg CONFIG_SH_IS_[A-Z0-9_]+      n \
        && setcfg CONFIG_SH_IS_ASH             y \
        && setcfg CONFIG_BASH_IS_[A-Z0-9_]+    n \
        && setcfg CONFIG_BASH_IS_NONE          y \
        && setcfg CONFIG_ASH                   y \
        && setcfg CONFIG_ASH_[A-Z0-9_]+        n \
        && setcfg CONFIG_ASH_PRINTF            y \
        && setcfg CONFIG_ASH_TEST              y \
        && setcfg CONFIG_AWK                   n \
        && setcfg CONFIG_TAIL                  y \
        && setcfg CONFIG_TAR                   y \
        && setcfg CONFIG_CAT                   n \
        && setcfg CONFIG_LS                    y \
        && setcfg CONFIG_ECHO                  n \
        && setcfg CONFIG_SLEEP                 n \
        && setcfg CONFIG_CHMOD                 n \
        && setcfg CONFIG_CHOWN                 n \
        && setcfg CONFIG_ID                    y \
        && setcfg CONFIG_MKDIR                 n \
        && setcfg CONFIG_MKPASSWD              n \
        && grep -v '^#' ./.config | sort | uniq
    RUN make -j "$(nproc)" && LDFLAGS="--static" make install
    RUN test -z "$(readelf -x .interp ./_install/bin/busybox 2>/dev/null)"
    RUN strip -s ./_install/bin/busybox

busybox-linux-amd64:
    FROM --platform=linux/amd64 +busybox
    SAVE ARTIFACT /tmp/busybox/busybox

busybox-linux-arm64:
    FROM --platform=linux/arm64 +busybox
    SAVE ARTIFACT /tmp/busybox/busybox

busybox-linux-arm-v6:
    FROM --platform=linux/arm/v6 +busybox
    SAVE ARTIFACT /tmp/busybox/busybox

busybox-linux-arm-v7:
    FROM --platform=linux/arm/v7 +busybox
    SAVE ARTIFACT /tmp/busybox/busybox

busybox-offline-payload-all:
    BUILD +busybox-linux-amd64
    BUILD +busybox-linux-arm64
    BUILD +busybox-linux-arm-v6
    BUILD +busybox-linux-arm-v7

offline-payload-util:
    FROM busybox
    WORKDIR /payload

offline-payload-save:
    FROM +offline-payload-util

    COPY +busybox-linux-amd64/busybox busybox-linux-amd64
    SAVE ARTIFACT busybox-linux-amd64 AS LOCAL internal/k8s/offline/payload/busybox-linux-amd64

    COPY +busybox-linux-arm64/busybox busybox-linux-arm64
    SAVE ARTIFACT busybox-linux-arm64 AS LOCAL internal/k8s/offline/payload/busybox-linux-arm64

    COPY +busybox-linux-arm-v6/busybox busybox-linux-arm-v6
    SAVE ARTIFACT busybox-linux-arm-v6 AS LOCAL internal/k8s/offline/payload/busybox-linux-arm-v6

    COPY +busybox-linux-arm-v7/busybox busybox-linux-arm-v7
    SAVE ARTIFACT busybox-linux-arm-v7 AS LOCAL internal/k8s/offline/payload/busybox-linux-arm-v7

offline-payload:
    FROM +offline-payload-util
    COPY +build/oci oci/.
    RUN tar -C /payload -czvf payload.tar.gz /payload
    SAVE ARTIFACT payload.tar.gz AS LOCAL dist/offline/offline-payload.tar.gz
