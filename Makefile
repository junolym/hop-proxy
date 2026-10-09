.PHONY: all server client agent web clean test docker check-wire check-fmt check-logattrs proto

REGISTRY ?= docker.nas.example.com

# === 版本号派生 ===
# 行为：
#   - 每次 make all 都构建并推送 :dev（覆盖，永远不报错）
#   - master 分支 + 正式发布（HEAD 恰好在 vX.Y.Z tag 上）：额外推送 :X.Y.Z 和 :latest
#     版本号 tag 的不可变性交给 registry 侧配置（如 Harbor tag immutability）
#   - 非 master 分支：额外推送 :<分支名> tag（如 :ui），不推 :latest / :版本号
#   - 二进制注入完整版本描述，便于线上排查：
#       release: X.Y.Z.时间戳.SHA
#       dev:     dev.时间戳.SHA
TIMESTAMP    := $(shell date +%Y%m%d.%H%M)
COMMIT       := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
GIT_DESCRIBE := $(shell git describe --tags --exact-match 2>/dev/null)
IS_RELEASE   := $(shell echo "$(GIT_DESCRIBE)" | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$$' >/dev/null 2>&1 && echo yes || echo no)
# 当前 git 分支：master 走正式发布逻辑，其他分支推 :<分支名> tag（如 :ui）
GIT_BRANCH   := $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)
IS_MASTER    := $(shell if [ "$(GIT_BRANCH)" = "master" ]; then echo yes; else echo no; fi)
# docker tag 只允许 [a-zA-Z0-9_.-]，把分支名里的非法字符（如 /）替换成 -
BRANCH_TAG   := $(shell echo "$(GIT_BRANCH)" | sed 's/[^a-zA-Z0-9._-]/-/g')
ifeq ($(IS_RELEASE),yes)
SEMVER  := $(shell echo "$(GIT_DESCRIBE)" | sed 's/^v//')
VERSION := $(SEMVER).$(TIMESTAMP).$(COMMIT)
else
VERSION := dev.$(TIMESTAMP).$(COMMIT)
endif
LDFLAGS  := -ldflags "-X main.buildVersion=$(VERSION) -X main.buildCommit=$(COMMIT)"

all: check-wire check-fmt check-logattrs web server client docker

# check-wire 守护：X-Hop-* 头名称字面量只允许出现在 pkg/tunnel（wire 层收口），
# pkg/ 不得依赖 internal/（分层边界）
check-wire:
	@if grep -rn '"X-Hop-' --include='*.go' cmd internal pkg | grep -v '^pkg/tunnel/' | grep -q .; then \
	  echo "违规：X-Hop-* 字面量只允许出现在 pkg/tunnel（统一收敛到 hopctx.go）"; \
	  grep -rn '"X-Hop-' --include='*.go' cmd internal pkg | grep -v '^pkg/tunnel/'; \
	  exit 1; \
	fi
	@if grep -rn 'hop-proxy/internal' pkg/ --include='*.go' | grep -q .; then \
	  echo "违规：pkg/ 不得依赖 internal/"; \
	  grep -rn 'hop-proxy/internal' pkg/ --include='*.go'; \
	  exit 1; \
	fi

# proto 重新生成请求上下文 wire schema（仅改动 pkg/tunnel/tunnelpb/*.proto 后需要；
# 生成物已提交仓库，日常构建与 CI 不需要 protoc）。
# 依赖：protoc（>= 28.x）与 protoc-gen-go（v1.36.x）在 PATH 中。
proto:
	protoc --go_out=. --go_opt=paths=source_relative -I. pkg/tunnel/tunnelpb/hopctx.proto

# check-logattrs 守护：代理链路日志的 request_id/original_path 字段只允许经
# HopContext/ReqContext.LogAttrs() 输出（hopctx.go 为收口点；popField 提取逻辑、
# json tag 与查询参数名除外）
check-logattrs:
	@if grep -rn '"request_id"\|"original_path"' --include='*.go' internal pkg \
	  | grep -v '^pkg/tunnel/hopctx.go' | grep -v 'popField' \
	  | grep -v 'json:' | grep -v '\.Get(\|\.Set(' | grep -q .; then \
	  echo "违规：request_id/original_path 日志字段应经 LogAttrs 输出（不要手写）"; \
	  grep -rn '"request_id"\|"original_path"' --include='*.go' internal pkg \
	    | grep -v '^pkg/tunnel/hopctx.go' | grep -v 'popField' \
	    | grep -v 'json:' | grep -v '\.Get(\|\.Set('; \
	  exit 1; \
	fi

# check-fmt 守护：Go 源码格式统一
check-fmt:
	@if [ -n "$$(gofmt -l cmd internal pkg)" ]; then \
	  echo "gofmt 未格式化："; gofmt -l cmd internal pkg; exit 1; \
	fi

web:
	cd web && npm run build
	rm -rf cmd/server/web/dist
	cp -r web/dist cmd/server/web/dist

server:
	CGO_ENABLED=0 go build $(LDFLAGS) -o bin/hop-proxy-server ./cmd/server

client:
	CGO_ENABLED=0 go build $(LDFLAGS) -o bin/hop-proxy-client ./cmd/client

agent:
	CGO_ENABLED=0 go build $(LDFLAGS) -o bin/hop-proxy-agent ./cmd/agent
	docker build -t $(REGISTRY)/hop-proxy-agent:latest -f Dockerfile.agent .
	docker push $(REGISTRY)/hop-proxy-agent:latest

clean:
	rm -rf bin/
	rm -rf web/dist
	rm -rf cmd/server/web/dist

# 集成测试（test/ 独立 module 的 Docker Compose 端到端黑盒测试）。
# 一条命令完成完整流程：重建二进制（测试镜像只 COPY bin/ 不编译）→ 清理旧环境
# （避免 TestMain 因环境已在运行而跳过 init/注册）→ TestMain 拉起环境并跑全部用例。
# 单跑某用例请直接：cd test && go test ./suites/ -run TestXxx -count=1
test: server client
	docker compose -f test/docker/docker-compose.yml down -v || true
	cd test && go test ./suites/ -count=1

docker: server client
	docker build -t $(REGISTRY)/hop-proxy-server:dev -f Dockerfile.server .
	docker build -t $(REGISTRY)/hop-proxy-client:dev -f Dockerfile.client .
	docker push $(REGISTRY)/hop-proxy-server:dev
	docker push $(REGISTRY)/hop-proxy-client:dev
ifeq ($(IS_MASTER),yes)
ifeq ($(IS_RELEASE),yes)
	docker tag $(REGISTRY)/hop-proxy-server:dev $(REGISTRY)/hop-proxy-server:$(SEMVER)
	docker tag $(REGISTRY)/hop-proxy-client:dev $(REGISTRY)/hop-proxy-client:$(SEMVER)
	docker push $(REGISTRY)/hop-proxy-server:$(SEMVER)
	docker push $(REGISTRY)/hop-proxy-client:$(SEMVER)
	docker tag $(REGISTRY)/hop-proxy-server:dev $(REGISTRY)/hop-proxy-server:latest
	docker tag $(REGISTRY)/hop-proxy-client:dev $(REGISTRY)/hop-proxy-client:latest
	docker push $(REGISTRY)/hop-proxy-server:latest
	docker push $(REGISTRY)/hop-proxy-client:latest
endif
else
	docker tag $(REGISTRY)/hop-proxy-server:dev $(REGISTRY)/hop-proxy-server:$(BRANCH_TAG)
	docker tag $(REGISTRY)/hop-proxy-client:dev $(REGISTRY)/hop-proxy-client:$(BRANCH_TAG)
	docker push $(REGISTRY)/hop-proxy-server:$(BRANCH_TAG)
	docker push $(REGISTRY)/hop-proxy-client:$(BRANCH_TAG)
endif
