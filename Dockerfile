# Build the manager binary
FROM golang:1.19.8 as builder

ARG arch
ARG CRYPTO_LIB
ENV GOEXPERIMENT=${CRYPTO_LIB:+boringcrypto}
WORKDIR /workspace
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

# Build

RUN if [ ${CRYPTO_LIB} ]; \
    then \
      CGO_ENABLED=1 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -a -ldflags "-linkmode=external -extldflags=-static"  -o manager main.go ;\
    else \
      CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -a -ldflags '-s -w' -o manager main.go ;\
    fi 
# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
