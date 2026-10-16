# cardhub — `make install` puts the cardhub command on your PATH (in $(go env GOPATH)/bin).

.PHONY: install build test doctor uninstall

install:
	go install .
	@bin=$$(go env GOBIN); [ -n "$$bin" ] || bin=$$(go env GOPATH)/bin; \
	echo "Installed $$bin/cardhub"; \
	case ":$$PATH:" in *":$$bin:"*) ;; *) echo; \
	  echo "That folder isn't on your PATH yet. Add this line to ~/.zshrc (or ~/.bashrc), then open a new terminal:"; \
	  echo "  export PATH=\"\$$PATH:$$bin\"";; esac; \
	echo; echo "Next: run 'cardhub doctor' to check your setup, then 'cardhub'."

build:
	go build -o cardhub .

test:
	go vet ./... && go test ./...

doctor: build
	./cardhub doctor

uninstall:
	rm -f "$$(go env GOPATH)/bin/cardhub"
