SHELL := /usr/bin/env bash

.DEFAULT_GOAL := test

.PHONY: test test-dev test-ci

# 开发入口：沿用核心回归门禁，并把 Go 并发限制在较小范围。
test: test-dev

test-dev:
	@GOMAXPROCS=2 GOFLAGS=-p=1 bash scripts/test-core-regression.sh connection-health

# CI 入口：由 GitHub Actions 调用；不继承本地开发限速。
test-ci:
	@CI=true bash scripts/test-full-regression.sh
