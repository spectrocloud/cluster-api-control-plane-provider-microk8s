ARG BUILDER_GOLANG_VERSION
# First stage: build the executable.
FROM --platform=$TARGETPLATFORM gcr.io/spectro-images-public/golang:${BUILDER_GOLANG_VERSION}-alpine as toolchain
# Run this with docker build --build_arg $(go env GOPROXY) to override the goproxy
ARG goproxy=https://proxy.golang.org
ENV GOPROXY=$goproxy

# FIPS
ARG CRYPTO_LIB
ENV GOEXPERIMENT=${CRYPTO_LIB:+boringcrypto}

FROM toolchain as builder
WORKDIR /workspace

RUN apk update

# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY main.go main.go
COPY api/ api/
COPY controllers/ controllers/
COPY pkg/ pkg/

# Build
ARG ARCH
ARG ldflags
RUN if [ ${CRYPTO_LIB} ]; \
    then \
      GOARCH=${ARCH} go-build-fips.sh -a -o manager main.go;\
    else \
      GOARCH=${ARCH} go-build-static.sh -a -o manager main.go;\
    fi
RUN if [ "${CRYPTO_LIB}" ]; then assert-static.sh manager; fi
RUN if [ "${CRYPTO_LIB}" ]; then assert-fips.sh manager; fi
RUN scan-govulncheck.sh manager

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
