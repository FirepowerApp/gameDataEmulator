FROM golang:1.23.3 AS builder

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o testserver ./cmd/testserver

# Use a minimal base image
FROM gcr.io/distroless/static

WORKDIR /

# Copy the binary from the builder stage
COPY --from=builder /app/testserver .

# Expose the default ports
EXPOSE 8124 8125

# Set default environment variables
ENV PLAYBYPLAY_PORT=8125
ENV STATS_PORT=8124

# Run the application
CMD ["/testserver"]
