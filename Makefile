BINARY := bin/bicz

.PHONY: build test vet fmt lint-fmt clean

build:
	go build -o $(BINARY) ./cmd/bicz

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

lint-fmt:
	@files="$$(gofmt -l .)"; \
	if [ -n "$$files" ]; then echo "Unformatted files:"; echo "$$files"; exit 1; fi

clean:
	rm -rf bin out dist