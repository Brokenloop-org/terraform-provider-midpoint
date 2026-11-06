TESTARGS ?= -run TestAcc

export MIDPOINT_URL ?= http://localhost:8080
export MIDPOINT_USERNAME ?= administrator
export MIDPOINT_PASSWORD ?= Test5ecr3t

.PHONY: build test testacc lint docs validate-examples env-up env-down schema-gen

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
# without a required_providers block gets one for brokenloop-org/midpoint. Output is
# shown only for a directory that fails.
EXAMPLE_DIRS = examples/complete examples/provider $(wildcard examples/resources/*) $(wildcard examples/data-sources/*)

validate-examples:
	go build -o ./dist/terraform-provider-midpoint .
	@printf 'provider_installation {\n  dev_overrides {\n    "brokenloop-org/midpoint" = "%s"\n  }\n  direct {}\n}\n' "$(CURDIR)/dist" > dist/terraformrc
	@set -e; export TF_CLI_CONFIG_FILE="$(CURDIR)/dist/terraformrc"; \
	for dir in $(EXAMPLE_DIRS); do \
		tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
		cp -R "$$dir/." "$$tmp/"; \
		if ! grep -qs required_providers "$$tmp"/*.tf; then \
			printf 'terraform {\n  required_providers {\n    midpoint = { source = "brokenloop-org/midpoint" }\n  }\n}\n' > "$$tmp/zz_required_providers.tf"; \
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

# schema-gen regenerates internal/mpschema/tables from the XSDs in the images
# listed in internal/mpschema/images.txt, one "<line> <image@digest>" per line.
schema-gen:
	@set -e; mkdir -p internal/mpschema/tables; while read -r line image <&3; do \
		[ -n "$$line" ] || continue; \
		tmp=$$(mktemp -d); container=$$(docker create "$$image"); \
		trap 'docker rm "$$container" >/dev/null; rm -rf "$$tmp"' EXIT; \
		docker cp -q "$$container:/opt/midpoint/lib/midpoint.jar" "$$tmp/midpoint.jar"; \
		unzip -q -j "$$tmp/midpoint.jar" 'BOOT-INF/lib/schema-*.jar' 'BOOT-INF/lib/prism-impl-*.jar' -d "$$tmp/jars"; \
		for jar in "$$tmp"/jars/*.jar; do unzip -q -o "$$jar" '*.xsd' -d "$$tmp/xsd"; done; \
		go run ./internal/mpschema/gen -line "$$line" -source "$$image" -xsd "$$tmp/xsd" -out "internal/mpschema/tables/$$line.json.gz"; \
		echo "ok   $$line $$image"; \
		docker rm "$$container" >/dev/null; rm -rf "$$tmp"; trap - EXIT; \
	done 3< internal/mpschema/images.txt
