# Ethereum Swarm Docker Image

This directory contains the Docker setup for running Ethereum Swarm nodes for comparison testing.

## Building the Image

```bash
docker build -t swarm-node:latest scripts/docker/swarm/
```

## Configuration

The Swarm node can be configured via environment variables:

- `SWARM_DATA_DIR`: Data directory (default: `/app/data`)
- `SWARM_API_ADDR`: API address and port (default: `0.0.0.0:8500`)
- `SWARM_BOOTNODE`: Bootnode address for joining existing network
- `SWARM_LOG_FILE`: Optional log file path

## Ports

- `8500`: HTTP API port
- `30399`: P2P networking port

## Usage Example

```bash
docker run -d \
  -p 8500:8500 \
  -p 30399:30399 \
  -e SWARM_API_ADDR=0.0.0.0:8500 \
  -v swarm-data:/app/data \
  swarm-node:latest
```

