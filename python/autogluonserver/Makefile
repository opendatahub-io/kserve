# RHEL UBI9 (glibc 2.34)
AIPCC_INDEX_URL ?= https://console.redhat.com/api/pypi/public-rhai/rhoai/3.5/cpu-ubi9/simple/
PYTHON_PLATFORM := x86_64-manylinux_2_34
PYTHON_VERSION := 3.12

dev_install:
	uv sync --active --group test

install_dependencies:
	uv sync --active --group test

requirements:
	echo "--index-url $(AIPCC_INDEX_URL)" > autogluon-all-requirements.txt
	echo "" >> autogluon-all-requirements.txt
	uv pip compile pyproject.toml build-requirements.in --generate-hashes --no-header \
		--no-emit-package kserve \
		--no-emit-package kserve-storage \
		--no-emit-package autogluonserver \
		--python-version $(PYTHON_VERSION) \
		--python-platform $(PYTHON_PLATFORM) \
		--index-url "$(AIPCC_INDEX_URL)" >> autogluon-all-requirements.txt

test: type_check
	pytest -W ignore

type_check:
	mypy --ignore-missing-imports autogluonserver
