.PHONY: build check dev

build:
	nix build

check:
	nix flake check --print-build-logs

dev:
	nix develop
