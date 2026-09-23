default: run

# Build the project
build:
	go build -o bin/reflex main.go
	go build -o bin/loadgen demo/loadgen.go

# Run the proxy server with Mock API
run:
	go run main.go

# Run the proxy server with a real Jev API key
run-real:
	@if [ -z "$$JEV_API_KEY" ]; then echo "Error: JEV_API_KEY environment variable not set"; exit 1; fi
	go run main.go

# Run the proxy server pointing to the local Laya microservice
run-laya:
	JEV_API_ENDPOINT=http://localhost:8081/v1/evaluate JEV_API_KEY=local go run main.go

# Start the Laya Python microservice (Runs on port 8081)
run-laya-server:
	cd laya-server && py -m pip install -r requirements.txt && py server.py

# Fire the load generator to simulate traffic (run this in a separate terminal)
load:
	go run demo/loadgen.go

# Run tests
test:
	go test -v ./...
