.PHONY: test run-controller run-runner

PYTHONPATH := src
export PYTHONPATH

test:
	python3 -m unittest discover -s tests -v

run-controller:
	POLIS_DB_PATH=$${POLIS_DB_PATH:-./polis.db} python3 -m polis.controller

run-runner:
	python3 -m polis.runner
