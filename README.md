# Adminer Teleport Go

A CLI tool that orchestrates Teleport database connections with Adminer web interfaces using Docker Compose.

## Overview

This tool streamlines database access by:
- Establishing Teleport proxy connections to remote databases
- Automatically generating Docker Compose configurations
- Deploying Adminer containers for each database
- Opening browser windows to pre-configured Adminer instances

## Prerequisites

- `tsh` (Teleport CLI) - logged in and authenticated
- `socat` - for port forwarding
- Docker with Compose support (`docker compose` or `docker-compose`)
  - Also supports `podman-compose` as an alternative
- Go 1.25.4 or later (for building from source)

## Setup

1. Copy the example settings file:
   ```bash
   cp settings.example.json settings.json
   ```

2. Configure your databases in `settings.json`:
   ```json
   {
     "databases": [
       {
         "name": "my_db",
         "cluster": "teleport-cluster-name",
         "db_system": "pgsql",
         "db_user": "username",
         "db_name": "database_name",
         "bridge_port": 5433,
         "adminer_port": 8081
       }
     ]
   }
   ```

### Configuration Fields

**Required fields:**
- `name`: Identifier for the database connection
- `cluster`: Teleport cluster name
- `db_system`: Database type (`pgsql` or `mysql`)
- `db_user`: Database username
- `bridge_port`: Local port for Teleport proxy
- `adminer_port`: Local port for Adminer web interface

**Optional fields:**
- `db_name`: Specific database name to connect to

## Usage

Start all configured databases:
```bash
go run . 
```

Start specific databases by name:
```bash
go run . database1 database2
```

Custom configuration file:
```bash
go run . -config /path/to/settings.json
```

Custom output directory for logs:
```bash
go run . -out /path/to/logs
```

## Building

```bash
go build -o adminer-teleport
./adminer-teleport
```

## How It Works

1. Validates prerequisites (`tsh`, Docker Compose)
2. Reads database configurations from `settings.json`
3. Generates a `compose.yml` file with Adminer services
4. Starts Teleport proxy connections using `tsh proxy db`
5. Launches Docker containers for each Adminer instance
6. Opens browser windows to Adminer interfaces with pre-filled connection details
7. Monitors for interrupt signals (Ctrl+C) and cleans up resources

## Output

- Teleport proxy logs: `output/<database-name>-tsh.log`
- Docker Compose file: `compose.yml` (auto-generated)
- Plugins: `plugins-enabled/` directory

## Adminer Authentication

The tool uses a passwordless login plugin for convenience. When accessing Adminer:
- **Password**: `a` (single character)
- This is configured in `plugins-enabled/login-password-less.php`
- You can modify the password by changing the hash in that file

## Cleanup

Press `Ctrl+C` to gracefully shut down all connections and containers. The tool automatically:
- Terminates Teleport proxy processes
- Stops and removes Docker containers
- Cleans up generated files

## Environment Variables

- `ADMINER_TELEPORT_SETTING_PATH`: Override default settings file path (default: `settings.json`)
- `ADMINER_TELEPORT_OUTPUT_DIR`: Override default output directory (default: `output`)

## License

See project repository for license information.
