# Test Server

This is a standalone test server that simulates the NHL API and MoneyPuck API for testing purposes. It provides the same functionality as the embedded test servers but runs independently in its own Docker container.

## Features

- **Play-by-Play API Simulation**: Mimics the NHL play-by-play API on port 8125
- **Statistics API Simulation**: Mimics the MoneyPuck statistics API on port 8124
- **Cycling Game Events**: Provides 10 predefined game events that cycle through
- **Configurable Ports**: Environment variable support for custom ports

## Quick Start

### Using Docker Compose (Recommended)

```bash
cd testserver
docker-compose up -d
```

This will:
- Build the test server image
- Start the container with both APIs running
- Expose ports 8124 (stats) and 8125 (play-by-play)
- Include health checks and restart policies

### Using Docker

```bash
cd testserver
docker build -t testserver .
docker run -p 8124:8124 -p 8125:8125 testserver
```

### Running Locally

```bash
cd testserver
go run ./cmd/testserver
```

## API Endpoints

### Play-by-Play API (Port 8125)
- **Endpoint**: `GET /v1/gamecenter/{gameId}/play-by-play`
- **Example**: `http://localhost:8125/v1/gamecenter/2024030411/play-by-play`

### Statistics API (Port 8124)
- **Endpoint**: `GET /moneypuck/gameData/20242025/{gameId}.csv`
- **Example**: `http://localhost:8124/moneypuck/gameData/20242025/2024030411.csv`

## Configuration

Environment variables:
- `PLAYBYPLAY_PORT`: Port for the play-by-play server (default: 8125)
- `STATS_PORT`: Port for the statistics server (default: 8124)

## Test Data

### Game Events (Cycling)
1. faceoff
2. shot-on-goal
3. blocked-shot
4. missed-shot
5. goal
6. hit
7. takeaway
8. giveaway
9. penalty
10. game-end

### Predefined Game Statistics
- Game `2024030411`: Home 2.35, Away 1.87
- Game `2024030412`: Home 3.12, Away 2.94
- Game `2024030413`: Home 1.95, Away 2.68
- Other games: Default values Home 2.50, Away 2.50

## Integration

To use this test server with the main application:

1. Start the test server container
2. Ensure the main application can reach `localhost:8124` and `localhost:8125`

## Development

To modify the test data:
1. Edit `internal/services/testdata.go`
2. Rebuild the Docker image
3. Restart the container

## Health Check

The container includes a health check that verifies the play-by-play API is responding:
```bash
wget --quiet --tries=1 --spider http://localhost:8125/v1/gamecenter/test/play-by-play
