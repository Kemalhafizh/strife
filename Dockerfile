# strife: the CLI (simulator, checker, live runner, attestation).
# Built for the target platform without emulation: Go cross-compiles.
FROM --platform=$BUILDPLATFORM golang:1.24-bookworm AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w -X github.com/kemalhafizh/strife/internal/version.Version=${VERSION}" \
    -o /out/strife ./cmd/strife \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/strife /usr/local/bin/strife
# strife server's default data directory, writable by the nonroot user so a
# named volume mounted there starts with the right owner.
COPY --from=build --chown=65532:65532 /out/data /var/lib/strife
EXPOSE 8080
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/strife"]
CMD ["help"]
