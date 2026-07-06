DOCKER_FLAG := $(findstring docker, $(MAKECMDGOALS))
HTML_FLAG := $(findstring html, $(MAKECMDGOALS))
MAKEFLAGS += --silent

build:
ifeq ($(DOCKER_FLAG),docker)
	docker-compose build
else
	go build ./...
endif

up:
ifeq ($(DOCKER_FLAG),docker)
	docker-compose up -d
else
	reflex -r '\.go$$' -s -- sh -c "go run cmd/api/main.go"
endif

down:
ifeq ($(DOCKER_FLAG),docker)
	docker-compose down
else
	echo "Not applicable locally. Stop the application manually."
endif

test_app:
ifeq ($(DOCKER_FLAG),docker)
	docker-compose --profile test run --rm -e "" ota-server-test go test -v -coverprofile=coverage.out ./...
else
	$(MAKE_COVERAGE_CMD)
endif

test_app_watch:
	find . -name '*.go' | entr -n -c $(MAKE) test_app $(DOCKER_FLAG) $(HTML_FLAG)


define MAKE_COVERAGE_CMD
	go test -v -coverprofile=coverage.out ./... && \
	$(call CLEAN_COVERAGE) && \
	$(call GENERATE_HTML)
endef

define CLEAN_COVERAGE
	if [ "$(shell uname -s)" = "Darwin" ]; then \
		sed -i '' -e '/test/d' -e '/cmd/d' coverage.out; \
	else \
		sed -i '/test/d;/cmd/d;' coverage.out; \
	fi
endef

define GENERATE_HTML
	if [ "$(HTML_FLAG)" = "html" ]; then \
		go tool cover -html=coverage.out -o coverage.html && \
		echo 'Coverage report generated: coverage.html'; \
	fi
endef

.PHONY: docker html

test_integration:
	docker compose up -d postgres
	TEST_DB_URL="postgres://postgres:secret@localhost:5432/expo_ota_dev?sslmode=disable" go test -tags=integration -count=1 -v ./test/integration/...
