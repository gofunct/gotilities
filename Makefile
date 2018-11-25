.PHONY: clean test

application = ping-serve
object = $(application)
commands = pinger
package = github.com/gofucnt/gotilities
bin = $(shell pwd)/bin

GO ?= go

GOTEST_FLAGS ?=
GO_SOURCES := $(shell find $(PWD)/example/server)
CMD_SOURCES := $(shell find $(PWD)/example/cmd)
GO_TEST_SOURCES := $(shell find $(PWD) -path $(PWD)/vendor -prune -o -name '*_test.go' -print)

build: $(application) $(commands) ## build go binaries

$(bin):
	mkdir -p $@
	@export PATH= $(env)

$(application): $(GO_SOURCES)
	@go vet
	@go build -o bin/$@

$(commands): $(CMD_SOURCES)
	@go build -o bin/$@ ./cmd/$@



test: $(GO_SOURCES) $(GO_TEST_SOURCES) ## run all tests
	@go test $(GOTEST_FLAGS) -cover ./...

clean: ## clean builds
	rm -rf $(bin)

ping: ## generate all protobufs in api/
		@protoc -I proto/ proto/ping/ping.proto --go_out=plugins=grpc:proto


serve: ## run certd locally for testing
	@bin/ping-serve

pinger: ## run certd locally for testing
	@bin/pinger


help:   ## Show this help.
	@fgrep -h "##" $(MAKEFILE_LIST) | fgrep -v fgrep | sed -e 's/\\$$//' | sed -e 's/##//'

