.PHONY: build build-ui build-go build-demo run dev-ui clean

build: build-ui build-go build-demo

build-ui:
	cd frontend && npm install && npm run build

build-go:
	go build -o qmetry .

build-demo:
	go build -o demo ./cmd/demo

run: build
	./qmetry

dev-ui:
	cd frontend && npm run dev

docker-up:
	docker compose --profile demo up -d --build

docker-down:
	docker compose --profile demo down

clean:
	rm -rf qmetry demo frontend/out frontend/.next frontend/node_modules
