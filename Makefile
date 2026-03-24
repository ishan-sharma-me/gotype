GOTYPE_HOME := $(HOME)/.gotype
BIN_DIR := $(GOTYPE_HOME)/bin
SHELL_RC := $(HOME)/.zshrc
PATH_LINE := export PATH="$$HOME/.gotype/bin:$$PATH"

VSCODE_EXT := editor/vscode

.PHONY: install install-lsp build clean uninstall test

install: build
	@mkdir -p $(BIN_DIR)
	@cp gotype $(BIN_DIR)/gotype
	@cp gotype-lsp $(BIN_DIR)/gotype-lsp
	@cp gotyped $(BIN_DIR)/gotyped
	@rm -f gotype gotype-lsp gotyped
	@echo "Installed gotype, gotype-lsp, gotyped to $(BIN_DIR)/"
	@if ! grep -qF '.gotype/bin' $(SHELL_RC) 2>/dev/null; then \
		echo '' >> $(SHELL_RC); \
		echo '# gotype' >> $(SHELL_RC); \
		echo '$(PATH_LINE)' >> $(SHELL_RC); \
		echo "Added $(BIN_DIR) to PATH in $(SHELL_RC)"; \
		echo "Run: source ~/.zshrc (or open a new terminal)"; \
	else \
		echo "PATH already configured in $(SHELL_RC)"; \
	fi

build:
	@go build -o gotype ./cmd/gotype/
	@go build -o gotype-lsp ./cmd/gotype-lsp/
	@go build -o gotyped ./cmd/gotyped/

clean:
	@rm -f gotype gotype-lsp gotyped

test:
	@go test ./...

test-conformance:
	@echo "Running Go spec conformance (2592 files)..."
	@go test ./conformance/ -run TestGoSpec -v -timeout 120s -count=1 2>&1 | grep -E "(PASS|FAIL|conformance:)"

test-kubernetes:
	@echo "Running Kubernetes conformance (16925 files — rename/transpile/parse/cleanup)..."
	@go test ./conformance/ -run TestKubernetes -v -timeout 600s -count=1 2>&1 | grep -E "(PASS|FAIL|SUMMARY|Total|Transpil|Parse|Cleanup|Found|Step|Renamed)"

install-lsp: install
	@echo "Building VS Code extension..."
	@cd $(VSCODE_EXT) && npm install --silent && npm run compile
	@echo "Installing VS Code extension..."
	@mkdir -p $(HOME)/.vscode/extensions
	@ln -sfn $(CURDIR)/$(VSCODE_EXT) $(HOME)/.vscode/extensions/gotype
	@echo "GoType extension installed. Restart VS Code to activate."

uninstall:
	@rm -f $(BIN_DIR)/gotype $(BIN_DIR)/gotype-lsp $(BIN_DIR)/gotyped
	@echo "Removed gotype, gotype-lsp, gotyped from $(BIN_DIR)"
