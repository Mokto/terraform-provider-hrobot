.PHONY: build release test test-unit test-acc
build:
	go build -o bin/terraform-provider-hrobot ./...
release:
	goreleaser release --clean

test: test-unit test-acc
test-unit:
	go test ./internal/... -v

test-acc:
	go test ./provider -v -timeout 20m
