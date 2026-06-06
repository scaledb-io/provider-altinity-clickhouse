HELM ?= helm

# values to override
IMAGE_PREFIX ?= ghcr.io/scaledb-io
PROVIDER_IMAGE_NAME ?= provider-clickhouse
VERSION ?= 0.1.0
CHART_VERSION ?= $(VERSION)

.PHONY: prepare-chart
prepare-chart:
	yq eval -i '.version = "$(CHART_VERSION)"' Chart.yaml
	yq eval -i '.appVersion = "$(VERSION)"' Chart.yaml
	yq eval -i '.image.repository = "$(IMAGE_PREFIX)/$(PROVIDER_IMAGE_NAME)"' values.yaml
	yq eval -i '.image.tag = "$(VERSION)"' values.yaml

.PHONY: release
release: prepare-chart

.PHONY: release-dev
release-dev: PROVIDER_IMAGE_NAME := provider-clickhouse-dev
release-dev: prepare-chart

.PHONY: deps
deps:
	$(HELM) version
