IMAGE := fs-mcp-server-test

.PHONY: build test test-container clean

build:
	go build -o bin/fs-mcp-server .

test:
	go test -v -count=1 ./...

test-container:
	podman build -t $(IMAGE) .
	podman run --rm $(IMAGE)

clean:
	rm -rf bin/
