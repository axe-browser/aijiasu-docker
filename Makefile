.PHONY: all build clean

all: build

build:
	go build -ldflags="-s -w" -o aijiasu ./cmd/aijiasu
	@echo "编译完成: ./aijiasu"

clean:
	rm -f aijiasu
