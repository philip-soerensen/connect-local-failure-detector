# Local development helpers. The marketplace builder checks Prusa Connect out
# into .core/ itself (core-ref input of the publish workflow).
CORE_REF ?= v0.1.0-beta.1

core:
	git clone --depth 1 --branch $(CORE_REF) https://github.com/prusa3d/prusa-connect.git .core

ui-build:
	cd ui && npm ci && npm run build
