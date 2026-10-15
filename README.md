# Oxide Monitor

A local bot monitoring dashboard and backend API built with Go, Fiber, PostgreSQL, and vanilla JavaScript.

## Overview

Oxide Monitor provides a local interface for monitoring bot status, sending bot commands, tracking command history, and managing order workflows.

### Features

- Retrieve bot information and synchronize live status from the Bot Simulator.
- Send `start`, `stop`, `restart`, and `status` commands to bots.
- Store bot commands and audit logs in PostgreSQL.
- Display bot status, CPU usage, memory usage, uptime, and command history.
- Create and track orders with idempotency checks and status-transition validation.
- Provide a system kill switch to temporarily disable new order creation.
- Include automated unit tests for order status transitions.

## Tech Stack

- **Backend:** Go, Fiber
- **Database:** PostgreSQL 16
- **Database driver:** pgx
- **Bot Simulator:** Node.js
- **Dashboard:** HTML, CSS, JavaScript
- **Local environment:** Docker Compose

## Prerequisites

Install the following tools:

- Go 1.26 or a compatible version
- Node.js
- Docker Desktop with Docker Compose
- Git

## Project Structure

```text
oxide-monitor-candidate-pack/
├── backend/
│   ├── main.go
│   ├── main_test.go
│   ├── go.mod
│   └── .env
├── dashboard/
│   ├── index.html
│   ├── style.css
│   └── app.js
├── db/
│   ├── schema.sql
│   └── seed.sql
├── simulator/
│   └── server.js
├── docker-compose.yml
├── .env.example
└── README.md
```

## Getting Started

### 1. Start PostgreSQL

Run the following commands from the project root:

```powershell
docker compose up -d postgres
```

Initialize the database schema:

```powershell
Get-Content -Raw .\db\schema.sql |
  docker compose exec -T postgres psql -U oxide -d oxide_monitor
```

Load the sample data:

```powershell
Get-Content -Raw .\db\seed.sql |
  docker compose exec -T postgres psql -U oxide -d oxide_monitor
```

Run the schema and seed commands only when initializing the database for the first time. The schema uses `IF NOT EXISTS`, but the seed file may insert duplicate records if executed repeatedly.

### 2. Configure Environment Variables

Create `backend/.env` with the following local development configuration:

```env
APP_PORT=3000
DATABASE_URL=postgres://oxide:oxide_dev@localhost:55432/oxide_monitor?sslmode=disable
SIMULATOR_URL=http://localhost:8788
```

These credentials are for local development only. Do not use production credentials in this project.

### 3. Start the Bot Simulator

Open a separate terminal in the project root:

```powershell
node .\simulator\server.js
```

The simulator runs at:

`http://localhost:8788`

Check the simulator:

```powershell
Invoke-RestMethod http://localhost:8788/bots
```

### 4. Start the Backend and Dashboard

Open another terminal:

```powershell
cd .\backend
go mod download
go run .
```

The backend API and dashboard are available at:

- Dashboard: `http://localhost:3000`
- Health check: `http://localhost:3000/health`
- Bot list: `http://localhost:3000/api/bots`

The dashboard is served by the Go backend, so no separate frontend development server is required.

## API Endpoints

| Method | Endpoint | Description |
|---|---|---|
| GET | `/health` | Check backend health |
| GET | `/api/bots` | Retrieve bot data from PostgreSQL |
| GET | `/api/bots/live` | Synchronize bot data from the simulator |
| POST | `/api/bots/:id/commands` | Send a command to a bot |
| GET | `/api/bots/:id/commands` | Retrieve command history |
| GET | `/api/system/kill-switch` | Retrieve kill-switch state |
| PUT | `/api/system/kill-switch` | Enable or disable the kill switch |
| GET | `/api/orders` | List orders |
| POST | `/api/orders` | Create an order |
| GET | `/api/orders/:id/events` | Retrieve order events |
| PATCH | `/api/orders/:id/status` | Update order status |

### Example Requests

Retrieve bots:

```powershell
Invoke-RestMethod http://localhost:3000/api/bots
```

Synchronize live bot data:

```powershell
Invoke-RestMethod http://localhost:3000/api/bots/live
```

Restart a bot:

```powershell
Invoke-RestMethod `
  -Method Post `
  -Uri "http://localhost:3000/api/bots/bot-01/commands" `
  -ContentType "application/json" `
  -Headers @{ "X-Actor" = "local-user" } `
  -Body '{"command":"restart"}'
```

Retrieve command history:

```powershell
Invoke-RestMethod `
  "http://localhost:3000/api/bots/bot-01/commands"
```

Create an order:

```powershell
Invoke-RestMethod `
  -Method Post `
  -Uri "http://localhost:3000/api/orders" `
  -ContentType "application/json" `
  -Body '{"externalOrderId":"DEMO-ORDER-001","amount":100,"currency":"THB"}'
```

Retrieve orders:

```powershell
Invoke-RestMethod http://localhost:3000/api/orders
```

## Testing

Run the Go test suite from the backend directory:

```powershell
cd .\backend
go test -v ./...
```

The unit tests cover valid and invalid order status transitions.

Additional API scenarios to verify manually:

- Unknown bot IDs return an error.
- Unsupported bot commands are rejected.
- Duplicate start or stop requests are handled according to the bot's current state.
- Command history records the requested command and its result.
- Creating an order with an existing external order ID and different order data returns a conflict.
- Invalid order status transitions are rejected.
- Enabling the kill switch prevents new orders from being created.

## Database and Environment Notes

- PostgreSQL runs locally on port `55432`.
- The Bot Simulator runs locally on port `8788`.
- The Go backend and dashboard run on port `3000`.
- Bot status and metrics are supplied by the simulator; CPU and memory values are mock data, not measurements of real machines.
- The kill-switch state is stored in application memory and resets when the backend restarts.
- The provided database schema is used without additional migrations.

## Constraints

- Use only the provided local data and services.
- Do not connect to production systems.
- Do not use the source code of the real Oxide Bot.
- Do not commit real passwords, tokens, or local environment files.

## Deliverables

- Backend source code
- Dashboard source code
- Database schema and seed files
- `README.md`
- `.env.example`
- Installation, run, and test instructions
