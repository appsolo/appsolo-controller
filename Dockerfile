# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.17 as builder

WORKDIR /src/
COPY go.* /src/
# Cache mod downloads
RUN go mod download -x

COPY cmd /src/cmd
COPY pkg /src/pkg

ARG GOOS=linux
ARG VERSION=v0.0.0-0.unknown

ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-X github.com/appsolo-com/appsolo-controller/pkg/consts.Version=${VERSION} -extldflags=-static"  -v ./cmd/...

FROM gcr.io/distroless/static:latest
COPY --from=builder /src/appsolo-controller /appsolo-controller
USER nonroot
ENTRYPOINT ["/appsolo-controller"]
