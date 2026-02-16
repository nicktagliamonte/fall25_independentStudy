# Swarm Setup Guide

This guide covers how to build, configure, and run Ethereum Swarm v0.5.8 nodes for comparison testing.

## Table of Contents

1. [Overview](#overview)
2. [Building the Docker Image](#building-the-docker-image)
3. [Configuration Options](#configuration-options)
4. [Starting Swarm Nodes](#starting-swarm-nodes)
5. [Network Configuration](#network-configuration)
6. [Known Issues and Workarounds](#known-issues-and-workarounds)
7. [Troubleshooting](#troubleshooting)
8. [Advanced Configuration](#advanced-configuration)

## Overview

This setup uses **Ethereum Swarm v0.5.8** (the legacy Swarm implementation, not the newer Bee client). The Docker image is built from source and configured to run in a containerized test environment.

### Key Components

- **Dockerfile**: Multi-stage build that compiles Swarm from source
- **Entrypoint Script**: Configures and starts Swarm nodes
- **Docker Compose Template**: Defines service configuration
- **Start Script**: Orchestrates multi-node cluster startup

### Architecture

- **Bootstrap Node**: First node in the cluster (IP: `172.20.0.200`)
- **Additional Nodes**: Additional nodes join the bootstrap (IPs: `172.20.0.201+`)
- **Shared Network**: Uses Docker network `fall25_independentstudy_node-network`
- **API Port**: Default `8500` (HTTP API)
- **P2P Port**: Default `30399` (peer-to-peer networking)

## Building the Docker Image

### Prerequisites

- Docker installed and running
- Internet connection (for downloading Swarm source code)
- Sufficient disk space (~2GB for build process)

### Quick Build

The image is automatically built when starting nodes:

```bash
./scripts/docker/swarm/start.sh 4
```

This will build the image if it doesn't exist.

### Manual Build

To build the image manually:

```bash
docker build -t swarm-node:latest scripts/docker/swarm/
```

### Build Process

The Dockerfile uses a multi-stage build:

1. **Builder Stage**:
   - Uses `golang:1.14-alpine` base image
   - Installs build dependencies (git, make, gcc, etc.)
   - Clones Swarm v0.5.8 repository from GitHub
   - Compiles Swarm binary using `make swarm`

2. **Runtime Stage**:
   - Uses `alpine:latest` base image
   - Installs runtime dependencies (ca-certificates, curl, jq)
   - Copies compiled Swarm binary
   - Sets up data and log directories
   - Copies entrypoint script

### Build Time

- **First build**: ~5-10 minutes (downloads dependencies and compiles)
- **Subsequent builds**: ~30 seconds (uses Docker cache)

### Verifying Build

After building, verify the image exists:

```bash
docker images | grep swarm-node
```

You should see:
```
swarm-node   latest   <image-id>   <size>   <time>
```

## Configuration Options

Swarm v0.5.8 uses command-line flags for configuration. These are set via environment variables in the Docker container.

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `SWARM_DATA_DIR` | `/app/data` | Directory for Swarm node data and keystore |
| `SWARM_HTTP_ADDR` | `0.0.0.0:8500` | HTTP API bind address (host:port) |
| `SWARM_HTTP_PORT` | `8500` | HTTP API port (if HTTP_ADDR not provided) |
| `SWARM_PASSWORD` | `swarm-test-password` | Password for account encryption |
| `SWARM_BOOTNODE` | (empty) | Bootstrap node address (enode:// format) |
| `SWARM_BZZ_ACCOUNT` | (empty) | BZZ account address (required if multiple accounts) |
| `SWARM_VERBOSITY` | `4` | Log verbosity level (0-6, higher = more verbose) |
| `SWARM_DEBUG` | `false` | Enable debug mode |

### Command-Line Flags

The entrypoint script converts environment variables to Swarm command-line flags:

- `--datadir`: Data directory
- `--password`: Password file path
- `--httpaddr`: HTTP API bind address (host)
- `--bzzport`: HTTP API port
- `--bootnodes`: Bootstrap node addresses
- `--bzzaccount`: BZZ account address
- `--verbosity`: Log verbosity level
- `--debug`: Debug mode flag

### Configuration Examples

#### Basic Configuration

```yaml
environment:
  - SWARM_DATA_DIR=/app/data
  - SWARM_HTTP_ADDR=0.0.0.0:8500
  - SWARM_PASSWORD=my-secure-password
```

#### With Bootnode

```yaml
environment:
  - SWARM_BOOTNODE=enode://PEER_ID@172.20.0.200:30399
```

#### High Verbosity

```yaml
environment:
  - SWARM_VERBOSITY=6
  - SWARM_DEBUG=true
```

## Starting Swarm Nodes

### Using the Start Script

The easiest way to start Swarm nodes:

```bash
# Start 4 nodes (1 bootstrap + 3 additional)
./scripts/docker/swarm/start.sh 4
```

This script:
1. Stops any existing Swarm containers
2. Ensures the shared network exists
3. Generates `docker-compose.swarm.yml` dynamically
4. Builds the Docker image (if needed)
5. Starts the bootstrap node
6. Waits for bootstrap to be ready
7. Starts additional nodes
8. Waits for all nodes to be ready

### Manual Docker Compose

If you prefer to use docker-compose directly:

```bash
# Build image
docker build -t swarm-node:latest scripts/docker/swarm/

# Start bootstrap
docker-compose -f docker-compose.swarm.yml up -d swarm-bootstrap

# Wait for bootstrap to be ready
docker-compose -f docker-compose.swarm.yml exec swarm-bootstrap curl -sf http://localhost:8500/

# Start additional nodes
docker-compose -f docker-compose.swarm.yml up -d swarm-node1 swarm-node2
```

### Node IP Addresses

Nodes are assigned IP addresses in the `172.20.0.0/16` subnet:

- **swarm-bootstrap**: `172.20.0.200`
- **swarm-node1**: `172.20.0.201`
- **swarm-node2**: `172.20.0.202`
- **swarm-node3**: `172.20.0.203`
- ... and so on

### Checking Node Status

```bash
# List running containers
docker-compose -f docker-compose.swarm.yml ps

# Check logs
docker-compose -f docker-compose.swarm.yml logs -f swarm-bootstrap

# Test API endpoint
curl http://172.20.0.200:8500/
```

### Stopping Nodes

```bash
# Stop all Swarm nodes
docker-compose -f docker-compose.swarm.yml stop

# Stop and remove containers
docker-compose -f docker-compose.swarm.yml down

# Stop and remove containers + volumes
docker-compose -f docker-compose.swarm.yml down -v
```

## Network Configuration

### Docker Network

Swarm nodes use a shared Docker network:

- **Network Name**: `fall25_independentstudy_node-network`
- **Subnet**: `172.20.0.0/16`
- **Driver**: `bridge`

### Network Creation

The network is created automatically by the start script. To create manually:

```bash
docker network create \
  --driver bridge \
  --subnet 172.20.0.0/16 \
  fall25_independentstudy_node-network
```

### Port Mapping

- **Bootstrap Node**: 
  - Host port `8500` → Container port `8500` (HTTP API)
  - Host port `30399` → Container port `30399` (P2P)
- **Additional Nodes**: 
  - No host port mapping (access via Docker network IPs)

### Network Troubleshooting

Check network configuration:

```bash
# Inspect network
docker network inspect fall25_independentstudy_node-network

# List all networks
docker network ls

# Check container network settings
docker inspect swarm-bootstrap | jq '.[0].NetworkSettings'
```

## Known Issues and Workarounds

### Issue 1: Bootnode Peer ID Extraction

**Problem**: Swarm v0.5.8 doesn't expose peer ID via HTTP API, making it difficult to configure bootnodes for additional nodes.

**Workaround**: 
- Bootstrap node uses placeholder peer ID: `enode://PLACEHOLDER_PEER_ID@172.20.0.200:30399`
- Swarm may still discover peers through other mechanisms
- For production, extract peer ID from logs or use Swarm's peer discovery

**Extracting Peer ID** (if needed):
```bash
# Check bootstrap logs for peer ID
docker logs swarm-bootstrap | grep -i "enode\|peer"

# Or check data directory
docker exec swarm-bootstrap cat /app/data/nodekey
```

### Issue 2: Address Already in Use

**Problem**: Error when starting multiple nodes: "Address already in use"

**Cause**: IP address conflicts with existing containers

**Solution**:
- Ensure IP addresses don't conflict (bootstrap: 200, nodes: 201+)
- Stop conflicting containers: `docker-compose down`
- Check for IP conflicts: `docker network inspect fall25_independentstudy_node-network`

### Issue 3: Bootstrap Node Fails to Start

**Problem**: Bootstrap node exits immediately or fails health checks

**Solutions**:
- Check logs: `docker logs swarm-bootstrap`
- Verify data directory permissions
- Check for port conflicts: `netstat -tuln | grep 8500`
- Ensure Docker has sufficient resources
- Try rebuilding the image: `docker build --no-cache -t swarm-node:latest scripts/docker/swarm/`

### Issue 4: Nodes Don't Connect to Bootstrap

**Problem**: Additional nodes start but don't connect to bootstrap

**Solutions**:
- Verify bootstrap is ready: `curl http://172.20.0.200:8500/`
- Check bootnode configuration in docker-compose
- Review network connectivity: `docker exec swarm-node1 ping -c 1 172.20.0.200`
- Check Swarm logs for connection errors
- Ensure nodes are on the same Docker network

### Issue 5: API Endpoint Not Accessible

**Problem**: Cannot access Swarm API from host machine

**Solutions**:
- Verify port mapping: `docker port swarm-bootstrap`
- Check firewall rules
- Use container IP instead of localhost: `curl http://172.20.0.200:8500/`
- Test from inside container: `docker exec swarm-bootstrap curl http://localhost:8500/`

### Issue 6: Build Failures

**Problem**: Docker build fails during compilation

**Solutions**:
- Check internet connection (needs to clone from GitHub)
- Verify Go version compatibility (requires Go 1.14)
- Check available disk space
- Try building with `--no-cache`: `docker build --no-cache -t swarm-node:latest scripts/docker/swarm/`
- Review build logs for specific errors

## Troubleshooting

### Common Commands

```bash
# Check container status
docker ps -a | grep swarm

# View logs
docker logs swarm-bootstrap
docker logs swarm-node1

# Follow logs in real-time
docker logs -f swarm-bootstrap

# Execute commands in container
docker exec -it swarm-bootstrap sh

# Check resource usage
docker stats swarm-bootstrap

# Inspect container configuration
docker inspect swarm-bootstrap

# Test API connectivity
curl -v http://172.20.0.200:8500/

# Check network connectivity
docker exec swarm-node1 ping -c 3 172.20.0.200
```

### Debugging Steps

1. **Verify Image Build**:
   ```bash
   docker images | grep swarm-node
   docker history swarm-node:latest
   ```

2. **Check Container Health**:
   ```bash
   docker ps --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}"
   ```

3. **Review Logs**:
   ```bash
   docker-compose -f docker-compose.swarm.yml logs --tail=100
   ```

4. **Test API Endpoints**:
   ```bash
   # From host
   curl http://172.20.0.200:8500/
   
   # From container
   docker exec swarm-bootstrap curl http://localhost:8500/
   ```

5. **Verify Network**:
   ```bash
   docker network inspect fall25_independentstudy_node-network
   ```

6. **Check Data Directories**:
   ```bash
   docker exec swarm-bootstrap ls -la /app/data
   docker exec swarm-bootstrap ls -la /app/logs
   ```

### Log Analysis

Swarm logs contain useful debugging information:

```bash
# Search for errors
docker logs swarm-bootstrap 2>&1 | grep -i error

# Search for peer connections
docker logs swarm-bootstrap 2>&1 | grep -i peer

# Search for API requests
docker logs swarm-bootstrap 2>&1 | grep -i "http\|api"

# Get last 50 lines
docker logs --tail 50 swarm-bootstrap
```

### Performance Issues

If nodes are slow or unresponsive:

1. **Check Resource Usage**:
   ```bash
   docker stats --no-stream
   ```

2. **Verify Disk Space**:
   ```bash
   df -h
   docker system df
   ```

3. **Check Network Latency**:
   ```bash
   docker exec swarm-node1 ping -c 5 172.20.0.200
   ```

4. **Review Verbosity**:
   - Lower verbosity (1-3) for better performance
   - Higher verbosity (5-6) for debugging

## Advanced Configuration

### Custom Docker Compose

Create a custom `docker-compose.swarm.yml`:

```yaml
version: '3.8'

services:
  swarm-bootstrap:
    build: scripts/docker/swarm
    image: swarm-node:latest
    container_name: swarm-bootstrap
    environment:
      - SWARM_DATA_DIR=/app/data
      - SWARM_HTTP_ADDR=0.0.0.0:8500
      - SWARM_PASSWORD=custom-password
      - SWARM_VERBOSITY=5
    volumes:
      - swarm-data:/app/data
      - swarm-logs:/app/logs
    networks:
      node-network:
        ipv4_address: 172.20.0.200
    ports:
      - "8500:8500"
      - "30399:30399"

networks:
  node-network:
    name: fall25_independentstudy_node-network
    external: true

volumes:
  swarm-data:
  swarm-logs:
```

### Persistent Data

Data is stored in Docker volumes:

```bash
# List volumes
docker volume ls | grep swarm

# Inspect volume
docker volume inspect swarm-bootstrap-data

# Backup volume
docker run --rm \
  -v swarm-bootstrap-data:/data \
  -v $(pwd):/backup \
  alpine tar czf /backup/swarm-backup.tar.gz /data

# Restore volume
docker run --rm \
  -v swarm-bootstrap-data:/data \
  -v $(pwd):/backup \
  alpine tar xzf /backup/swarm-backup.tar.gz -C /
```

### Multi-Network Setup

For testing across different networks:

```yaml
networks:
  network1:
    driver: bridge
    ipam:
      config:
        - subnet: 172.20.0.0/16
  network2:
    driver: bridge
    ipam:
      config:
        - subnet: 172.21.0.0/16
```

### Custom Entrypoint

Modify `scripts/docker/swarm/entrypoint.sh` to add custom Swarm flags or initialization logic.

### Environment-Specific Configuration

Use different configurations for different environments:

```bash
# Development
export SWARM_VERBOSITY=6
export SWARM_DEBUG=true

# Production
export SWARM_VERBOSITY=2
export SWARM_DEBUG=false
```

## Version Information

- **Swarm Version**: v0.5.8
- **Go Version**: 1.14
- **Base Image**: alpine:latest
- **API Port**: 8500
- **P2P Port**: 30399

## Additional Resources

- **Swarm Repository**: https://github.com/ethersphere/swarm
- **Swarm Documentation**: See Swarm v0.5.8 documentation
- **Test Documentation**: See `docs/SWARM_COMPARISON_TESTS.md`
- **Validation Script**: `scripts/validation/validate_swarm_setup.sh`

## Support

For issues specific to this setup:

1. Check this guide's troubleshooting section
2. Review Swarm logs: `docker logs <container-name>`
3. Run validation: `./scripts/validation/validate_swarm_setup.sh`
4. Check test documentation: `docs/SWARM_COMPARISON_TESTS.md`

---

**Last Updated**: 2026-02-16  
**Swarm Version**: v0.5.8
