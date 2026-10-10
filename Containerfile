# --- Stage 1: Build the Go application ---
FROM docker.io/library/golang:1.27.2-alpine AS builder

# Set the working directory inside the container
WORKDIR /app

# Copy dependency files first to leverage Docker build caching
COPY src/go.mod src/go.sum ./
RUN go mod download

# Copy the rest of the application source code
COPY src/ .

# Timezone for build number generation
ENV TZ=Europe/Berlin

# Build a statically linked, production-optimized binary
RUN go_var_path="main.BuildNumber" && \
    build_number=$(date +'%Y-%m-%dT%H:%M:%SZ') && \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s -X $go_var_path=$build_number" -o wallbox-controller .

# --- Stage 2: Final lightweight image ---
FROM alpine:latest

WORKDIR /app

# Timezone data for the container
RUN apk add --no-cache tzdata

# Copy the compiled binary from the builder stage
COPY --from=builder /app/wallbox-controller .

# Expose port
EXPOSE 8084

# Run the binary
CMD ["./wallbox-controller"]
