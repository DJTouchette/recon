.PHONY: build test bench lint clean profile

build:
	go build -o bin/recon ./cmd/recon

test:
	go test ./... -count=1

bench:
	go test -bench=. -benchmem ./...

lint:
	golangci-lint run

clean:
	rm -rf bin/ .recon/

profile:
	go test -bench=BenchmarkFullScan -benchmem -cpuprofile=cpu.prof -memprofile=mem.prof ./pkg/recon/
	go tool pprof -http=:6060 cpu.prof
