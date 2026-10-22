TESTARGS ?= -run TestAcc

export MIDPOINT_URL ?= http://localhost:8080
export MIDPOINT_USERNAME ?= administrator
export MIDPOINT_PASSWORD ?= Test5ecr3t

.PHONY: build test testacc lint docs validate-examples env-up env-down

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
	go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs generate --provider-name terraform-provider-midpoint

# validate-examples builds the provider into dist/ and runs terraform init and
# validate on every example directory against it, through a dev_overrides CLI
# config. Each directory is validated standalone, in a temporary copy; one
# without a required_providers block gets one for jplana/midpoint. Output is
# shown only for a directory that fails.
EXAMPLE_DIRS = examples/complete examples/provider $(wildcard examples/resources/*) $(wildcard examples/data-sources/*)

validate-examples:
	go build -o ./dist/terraform-provider-midpoint .
	@printf 'provider_installation {\n  dev_overrides {\n    "jplana/midpoint" = "%s"\n  }\n  direct {}\n}\n' "$(CURDIR)/dist" > dist/terraformrc
	@set -e; export TF_CLI_CONFIG_FILE="$(CURDIR)/dist/terraformrc"; \
	for dir in $(EXAMPLE_DIRS); do \
		tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
		cp -R "$$dir/." "$$tmp/"; \
		if ! grep -qs required_providers "$$tmp"/*.tf; then \
			printf 'terraform {\n  required_providers {\n    midpoint = { source = "jplana/midpoint" }\n  }\n}\n' > "$$tmp/zz_required_providers.tf"; \
		fi; \
		terraform -chdir="$$tmp" init -backend=false -input=false -no-color > "$$tmp/init.log" 2>&1 || { cat "$$tmp/init.log"; exit 1; }; \
		terraform -chdir="$$tmp" validate -no-color > "$$tmp/validate.log" 2>&1 || { echo "FAIL $$dir"; cat "$$tmp/validate.log"; exit 1; }; \
		echo "ok   $$dir"; \
		rm -rf "$$tmp"; \
	done

env-up:
	docker compose -f testacc/docker-compose.yml up -d && ./testacc/wait.sh

env-down:
	docker compose -f testacc/docker-compose.yml down -v
