# syntax=docker/dockerfile:1

# Stage 1: build
# Use the same Go version as go.mod (go 1.25.6 maps to toolchain 1.25.x).
# CGO_ENABLED=0: modernc.org/sqlite is pure-Go; no C toolchain needed.
FROM golang:1.25-alpine AS builder

WORKDIR /build

# Cache module downloads before copying source.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build the dashboard binary — static, no CGO, for a minimal runtime image.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" \
    -o /build/dashboard ./cmd/dashboard

# Stage 2: runtime
# distroless/static is a minimal image with no shell, no package manager,
# and no libc — appropriate for a statically-linked binary with pure-Go SQLite.
# nonroot variant runs as uid 65534 (nobody) by default.
FROM gcr.io/distroless/static:nonroot

# Copy the compiled binary.
COPY --from=builder /build/dashboard /dashboard

# The container runs as nonroot (uid 65534) inherited from the distroless base.
# No USER directive needed — distroless/static:nonroot already sets USER 65534.

EXPOSE 8080

ENTRYPOINT ["/dashboard"]
