all build:
	CGO_ENABLED=0 GOOS=linux go build -a -ldflags '-extldflags "-static"' -o hostpath-resizer ./cmd
.PHONY: all build

clean:
	rm -f hostpath-resizer
.PHONY: clean
