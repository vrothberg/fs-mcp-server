FROM registry.access.redhat.com/hi/go:latest-builder
WORKDIR /test
COPY go.mod go.sum ./
RUN go mod download
COPY . .
CMD ["go", "test", "-v", "-count=1", "./..."]
