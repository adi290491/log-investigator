.PHONY: build data test bench python-env clean

# All Go commands: bin/loggen, bin/scripted, bin/bench, and bin/server and
# bin/agent once they exist.
build:
	go build -o bin/ ./cmd/...

# Regenerate the committed synthetic dataset (deterministic).
data:
	go run ./cmd/loggen -out data

test:
	go vet ./... && go test ./...

# Python comparison server environment (uv if installed, else venv + pip).
python-env:
	compare/python/setup.sh

# Measure Go and Python servers; writes compare/RESULTS.md.
bench:
	compare/bench.sh

clean:
	rm -rf bin/ compare/python/.venv
