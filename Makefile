BINARY := bin/bicz

.PHONY: build test vet fmt lint-fmt bench clean

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

bench:
	PYTHONDONTWRITEBYTECODE=1 python3 enforcement_contracts.py

clean:
	rm -rf bin out dist