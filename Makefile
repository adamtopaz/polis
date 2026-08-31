CONTROLLER_GEN = go run sigs.k8s.io/controller-tools/cmd/controller-gen
CRD_OPTIONS = crd:generateEmbeddedObjectMeta=true

.PHONY: all build check deploy dev fmt generate install manifests test undeploy uninstall

all: build

build: manifests generate fmt
	go vet ./...
	go build ./...

check:
	nix flake check --print-build-logs

dev:
	nix develop

fmt:
	go fmt ./...

generate:
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

manifests:
	$(CONTROLLER_GEN) rbac:roleName=polis-controller $(CRD_OPTIONS) paths="./..." output:crd:artifacts:config=config/crd/bases

test: manifests generate fmt
	go vet ./...
	go test ./...

install: manifests
	kubectl apply --server-side -k config/crd

uninstall: manifests
	kubectl delete --ignore-not-found -k config/crd

deploy: manifests
	kubectl apply --server-side -k config/default

undeploy:
	kubectl delete --ignore-not-found -k config/default
