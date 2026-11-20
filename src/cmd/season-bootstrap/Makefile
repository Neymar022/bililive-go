# 从仓库根执行；只构建独立season工具，不改主服务/A32入口。
OUTPUT ?= bin/season-bootstrap
.PHONY: build
build:
	@mkdir -p "$(dir $(OUTPUT))"
	CGO_ENABLED=0 $(if $(PLATFORM),GOOS=$(PLATFORM),) $(if $(ARCH),GOARCH=$(ARCH),) go build -tags season_bootstrap_tool -o "$(OUTPUT)" ./src/cmd/season-bootstrap
