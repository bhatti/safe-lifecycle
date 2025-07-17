# --- Builder Stage ---
FROM --platform=linux/amd64 golang:1.23-alpine AS builder
WORKDIR /app

# Install wget and ca-certificates for secure downloads
RUN apk --no-cache add wget ca-certificates

# Download grpc-health-probe using wget (more reliable than curl)
RUN GRPC_HEALTH_PROBE_VERSION=v0.4.39 && \
    wget -qO /bin/grpc_health_probe \
    "https://github.com/grpc-ecosystem/grpc-health-probe/releases/download/${GRPC_HEALTH_PROBE_VERSION}/grpc_health_probe-linux-amd64" && \
    chmod +x /bin/grpc_health_probe

# Copy all source files including the api directory
COPY go.mod go.sum ./
RUN go mod download

# Copy all Go source files and the api directory
COPY . ./

# Build the application for linux/amd64
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /app/server .

# --- Final Stage ---
FROM --platform=linux/amd64 alpine:3.18

# Install ca-certificates
RUN apk add --no-cache ca-certificates

# Copy the health probe and the application binary from the builder stage
COPY --from=builder /bin/grpc_health_probe /bin/grpc_health_probe
COPY --from=builder /app/server /app/server

# Expose gRPC port and management port
EXPOSE 8085
EXPOSE 9090

# Set the entrypoint
ENTRYPOINT ["/app/server"]
