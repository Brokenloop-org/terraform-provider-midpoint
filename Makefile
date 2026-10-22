TESTARGS ?= -run TestAcc

export MIDPOINT_URL ?= http://localhost:8080
export MIDPOINT_USERNAME ?= administrator
export MIDPOINT_PASSWORD ?= 5ecr3t

.PHONY: build test testacc lint docs env-up env-down

build:
	go build ./...

test:
	go test ./...

testacc:
	TF_ACC=1 go test ./internal/provider/ $(TESTARGS) -timeout 60m -v

lint:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	go vet ./...

docs:
	go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs generate

env-up:
	@echo "env-up: see Task 3"

env-down:
	@echo "env-down: see Task 3"
