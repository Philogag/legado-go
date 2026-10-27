BINARY := bin/legado-server
PKG := ./cmd/server
FIXTURE := bin/fixtureserver
IMAGE ?= legado-go:local
PLATFORMS ?= linux/amd64,linux/arm64

.PHONY: all build fixture run test vet fmt cover smoke example clean docs \
        docker-build docker-multi docker-run docker-smoke docker-stop compose-up compose-down

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

# ---------------------------------------------------------------- 容器相关
docker-build: ## 本地构建镜像（当前架构）
	docker build --build-arg VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev) \
	  -t $(IMAGE) .

docker-multi: ## 多架构构建（需 buildx；不推送，产出 OCI 归档）
	docker buildx build --platform $(PLATFORMS) \
	  --build-arg VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev) \
	  -t $(IMAGE) --output type=oci,dest=/tmp/legado-go-oci.tar .
	@echo "OCI 归档: /tmp/legado-go-oci.tar"

docker-run: docker-build ## 前台运行镜像（只读 + 非 root）
	docker run --rm -p 8080:8080 --read-only --tmpfs /tmp \
	  --cap-drop ALL --security-opt no-new-privileges:true $(IMAGE)

docker-smoke: docker-build ## 容器冒烟：/healthz /readyz /mcp + 内置健康检查
	@docker rm -f legado-smoke >/dev/null 2>&1 || true
	docker run -d --name legado-smoke -p 18089:8080 $(IMAGE) >/dev/null
	@sleep 7  # 等 HEALTHCHECK 的 start-period(5s) 过去
	@echo -n "healthz="; curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:18089/healthz
	@echo -n "readyz=";  curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:18089/readyz
	@echo "--- MCP tools/list"; \
	  curl -s -X POST http://127.0.0.1:18089/mcp -H 'content-type: application/json' \
	    -H 'MCP-Protocol-Version: 2026-07-28' \
	    -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | head -c 200; echo
	@docker inspect -f 'health={{.State.Health.Status}}' legado-smoke
	@docker rm -f legado-smoke >/dev/null

docker-stop: ## 删除冒烟容器
	-docker rm -f legado-smoke

compose-up: ## docker compose 构建并启动
	docker compose up -d --build
	docker compose ps

compose-down: ## 停止并删除 compose 栈
	docker compose down
