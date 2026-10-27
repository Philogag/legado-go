BINARY := bin/legado-server
PKG := ./cmd/server
FIXTURE := bin/fixtureserver

.PHONY: all build fixture run test vet fmt cover smoke example clean docs

all: vet test build

build: ## 编译服务端二进制到 bin/legado-server
	@mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o $(BINARY) $(PKG)
	@echo "built $(BINARY)"

fixture: ## 编译本地假书站到 bin/fixtureserver
	@mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o $(FIXTURE) ./tools/fixtureserver

run: build fixture ## 本地起假书站 + 微服务（Ctrl-C 结束）
	@echo "fake site : http://127.0.0.1:18081"
	@echo "api       : http://127.0.0.1:18080"
	@$(FIXTURE) -addr 127.0.0.1:18081 & \
	 sleep 0.3; \
	 ADDR=127.0.0.1:18080 DEFAULT_TTL=10m $(BINARY); \
	 kill %1 2>/dev/null || true

test: ## 跑全部单元测试
	go test ./...

vet: ## 静态检查
	go vet ./...

fmt: ## 格式化
	gofmt -l -w .

cover: ## 带覆盖率的测试报告（coverage.html）
	go test -coverprofile=coverage.out ./... >/dev/null
	go tool cover -func=coverage.out | tail -1
	go tool cover -html=coverage.out -o coverage.html
	@echo "open coverage.html"

example: ## 跑 examples/library_usage.go.txt（库用法，需本地假书站）
	@mkdir -p .tmp-example && cp examples/library_usage.go.txt .tmp-example/main.go
	@$(FIXTURE) -addr 127.0.0.1:18081 >/tmp/legado-fixture.log 2>&1 & \
	 sleep 0.5; go run ./.tmp-example; st=$$?; kill %1 2>/dev/null; rm -rf .tmp-example; exit $$st

smoke: ## 端到端冒烟（REST + MCP + TTL）
	./scripts/smoke.sh

docs: ## 列出文档
	@ls -1 docs/*.md README.md

clean:
	rm -rf bin coverage.out coverage.html
