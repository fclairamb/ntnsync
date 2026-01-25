.PHONY: build clean test run sync tidy intercept

BINARY=ntnsync
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
GIT_TIME ?= $(shell TZ=UTC git log -1 --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo "unknown")
LDFLAGS := -X 'github.com/fclairamb/ntnsync/internal/version.Version=$(VERSION)' \
           -X 'github.com/fclairamb/ntnsync/internal/version.Commit=$(COMMIT)' \
           -X 'github.com/fclairamb/ntnsync/internal/version.GitTime=$(GIT_TIME)'

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

clean:
	rm -f $(BINARY)

test:
	go test ./...

run: build
	./$(BINARY)

sync: build
	./$(BINARY) sync --full

tidy:
	go mod tidy

intercept:
	./scripts/intercept.sh
