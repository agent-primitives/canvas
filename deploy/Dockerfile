# Tags are for readers; the digests are what builds actually use.
FROM golang:1.27.2-alpine@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY examples ./examples
COPY web ./web
RUN CGO_ENABLED=0 go build -trimpath -o /out/canvas ./cmd/canvas && \
    CGO_ENABLED=0 go build -trimpath -o /out/sessions-host ./cmd/sessions-host && \
    CGO_ENABLED=0 go build -trimpath -o /out/bundle-harness ./cmd/bundle-harness && \
    CGO_ENABLED=0 go build -trimpath -o /out/canvas-harness ./cmd/canvas-harness

FROM alpine:3.23@sha256:85fe1e81d6758c208f3e1eed4338a1997e19d4be002d4dd32d3100c9a8c010a0 AS producer
RUN adduser -D -u 65532 canvas
WORKDIR /data
COPY --from=build /out/canvas-harness /usr/local/bin/
USER 65532:65532
ENTRYPOINT ["canvas-harness"]

FROM alpine:3.23@sha256:85fe1e81d6758c208f3e1eed4338a1997e19d4be002d4dd32d3100c9a8c010a0 AS app
RUN apk add --no-cache git curl ca-certificates && adduser -D -u 65532 canvas
WORKDIR /app
COPY --from=build /out/canvas /out/sessions-host /usr/local/bin/
COPY examples ./examples
COPY model.lock.json model.config.json ./
USER 65532:65532
ENTRYPOINT ["canvas"]
