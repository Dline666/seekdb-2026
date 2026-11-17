GOLANGCI_LINT ?= golangci-lint

.PHONY: fmt lint lint-version

lint-version:
	@$(GOLANGCI_LINT) version --short | grep -qx '2.15.0' || \
		(echo 'Install golangci-lint v2.15.0; see README.md.' >&2; exit 1)

fmt: lint-version
	$(GOLANGCI_LINT) fmt ./...

lint: lint-version
	$(GOLANGCI_LINT) run ./...
