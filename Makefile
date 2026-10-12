.PHONY: build test test-short lint chaos clean

build:
	go build -o bin/strife ./cmd/strife

test:
	go test ./...

test-short:
	go test -race -short ./...

lint:
	test -z "$$(gofmt -l .)"
	go vet ./...

# The same gauntlet CI runs.
chaos: build
	./bin/strife sim --system raft --seeds 1..200 --determinism --expect linearizable --out ""
	./bin/strife sim --system raft-stale-reads --faults partition --seeds 1..50 --expect violation --out ""
	./bin/strife sim --system raft-lease --faults clock-skew,partition --seeds 1..200 --expect violation --out ""
	./bin/strife sim --system raft-unsafe-fsync --faults kill,io-stall --seeds 1..200 --expect violation --out ""

clean:
	rm -rf bin strife-out
