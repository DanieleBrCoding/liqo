# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /workspace

# Copy go mod and sum files
COPY go.mod go.mod
COPY go.sum go.sum

# Cache deps before building and copying source
RUN go mod download

# Copy the go source
COPY cmd/toy-labeler-controller/main.go main.go

# Build with ping support
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -ldflags="-w -s" -o toy-labeler-controller .

# Runtime stage
FROM alpine:3.18

RUN apk add --no-cache ca-certificates iputils net-tools bash

WORKDIR /

# Copy binary from builder
COPY --from=builder /workspace/toy-labeler-controller .

ENTRYPOINT ["/toy-labeler-controller"]
