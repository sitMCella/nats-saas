# syntax=docker/dockerfile:1
#
# docs/nats-auth-middleware/design.md §6. Multi-stage: the builder stage
# compiles a static binary; the final distroless stage carries only that
# binary, no Go toolchain, no shell, no package manager (INV-5, INV-6).
# Both base images are pinned by digest, amd64, so a rebuild of the same
# commit never silently picks up a new, unreviewed base image.

FROM golang:1.25-bookworm@sha256:c268a04d59aea0b180ed9946a658cfab9e7b3391dc90eed6e4969ccff98c851f AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/nats-auth-middleware ./cmd/nats-auth-middleware

FROM gcr.io/distroless/static-debian12:nonroot@sha256:52dcfbabb7457ea47c82f6e13af8c8a4a1d9f7b0145142b3ecab20f2b888411d
COPY --from=build /out/nats-auth-middleware /nats-auth-middleware
USER nonroot:nonroot
ENTRYPOINT ["/nats-auth-middleware"]
