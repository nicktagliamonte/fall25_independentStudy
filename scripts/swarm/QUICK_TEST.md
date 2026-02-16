# Swarm v0.5.8 API Quick Test Guide

## Quick Start

1. **Start Swarm node:**
   ```bash
   ./scripts/docker/swarm/start.sh 1
   ```

2. **Source API functions:**
   ```bash
   source scripts/swarm/api.sh
   ```

3. **Set API address:**
   ```bash
   API="http://172.20.0.200:8500"
   ```

## Basic Operations

### Test Node Connectivity
```bash
curl -s "$API/" | jq .
```

### Upload a File
```bash
echo "Hello Swarm!" > /tmp/test.txt
hash=$(upload_file "$API" "/tmp/test.txt")
echo "Uploaded hash: $hash"
```

### Download a File
```bash
download_file "$API" "$hash" "/tmp/downloaded.txt"
cat /tmp/downloaded.txt
```

### Check Content Availability
```bash
check_content "$API" "$hash" && echo "Content available" || echo "Content not available"
```

## Comprehensive Test Suite

Run the full test suite:
```bash
./scripts/swarm/test_api.sh [api_address]
```

Example:
```bash
./scripts/swarm/test_api.sh http://172.20.0.200:8500
```

## Manual API Testing

### Upload via curl
```bash
# Upload a file
curl -X POST -F "file=@/path/to/file.txt" "$API/bzz"

# Upload raw data
curl -X POST --data-binary @/path/to/file.txt "$API/bzz-raw"
```

### Download via curl
```bash
# Download via bzz
curl "$API/bzz:/<hash>"

# Download raw
curl "$API/bzz-raw:/<hash>"
```

## Troubleshooting

### Check Node Status
```bash
docker-compose -f docker-compose.swarm.yml ps
docker-compose -f docker-compose.swarm.yml logs swarm-bootstrap
```

### Test API Endpoint
```bash
curl -v "$API/"
```

### Check Network Connectivity
```bash
docker-compose -f docker-compose.swarm.yml exec swarm-bootstrap curl -s http://localhost:8500/
```
