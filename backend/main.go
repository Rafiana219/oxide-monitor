package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

func canTransition(from, to string) bool {
	switch from {
	case "pending":
		return to == "processing" ||
			to == "failed" ||
			to == "cancelled"

	case "processing":
		return to == "completed" ||
			to == "failed" ||
			to == "cancelled"

	default:
		return false
	}
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found; using system environment")
	}

	appPort := os.Getenv("APP_PORT")
	if appPort == "" {
		appPort = "3000"
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		log.Fatalf("create database pool: %v", err)
	}
	defer db.Close()

	if err := db.Ping(ctx); err != nil {
		log.Fatalf("connect to database: %v", err)
	}

	log.Println("Connected to PostgreSQL")

	app := fiber.New()
	var killSwitch atomic.Bool

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"status":  "ok",
			"service": "oxide-monitor",
		})
	})

	// Get the global order kill-switch status.
	app.Get("/api/system/kill-switch", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"enabled": killSwitch.Load(),
		})
	})

	// Enable or disable the global order kill-switch.
	app.Put("/api/system/kill-switch", func(c *fiber.Ctx) error {
		var payload struct {
			Enabled *bool `json:"enabled"`
		}

		if err := c.BodyParser(&payload); err != nil ||
			payload.Enabled == nil {
			return fiber.NewError(
				fiber.StatusBadRequest,
				"enabled must be a boolean",
			)
		}

		killSwitch.Store(*payload.Enabled)

		actor := c.Get("X-Actor")
		if actor == "" {
			actor = "local-user"
		}

		ctx, cancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cancel()

		metadata, _ := json.Marshal(fiber.Map{
			"enabled": *payload.Enabled,
		})

		_, err := db.Exec(ctx, `
			INSERT INTO audit_logs
				(actor, action, target_type, target_id, metadata)
			VALUES ($1, $2, 'system', 'kill-switch', $3::jsonb)
		`, actor, "system.kill_switch.updated", string(metadata))
		if err != nil {
			log.Printf("audit kill-switch update: %v", err)
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"kill-switch changed but audit log failed",
			)
		}

		return c.JSON(fiber.Map{
			"enabled": killSwitch.Load(),
		})
	})

	app.Get("/api/bots", func(c *fiber.Ctx) error {
		queryCtx, cancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cancel()

		rows, err := db.Query(queryCtx, `
			SELECT id, name, device_name, status,
			       cpu_percent, memory_percent, uptime_seconds,
			       last_heartbeat
			FROM oxide_bot_agents
			ORDER BY id
		`)
		if err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"failed to query bots",
			)
		}
		defer rows.Close()

		type Bot struct {
			ID            string     `json:"id"`
			Name          string     `json:"name"`
			DeviceName    string     `json:"deviceName"`
			Status        string     `json:"status"`
			CPUPercent    float64    `json:"cpuPercent"`
			MemoryPercent float64    `json:"memoryPercent"`
			UptimeSeconds int64      `json:"uptimeSeconds"`
			LastHeartbeat *time.Time `json:"lastHeartbeat"`
		}

		bots := make([]Bot, 0)

		for rows.Next() {
			var bot Bot
			if err := rows.Scan(
				&bot.ID,
				&bot.Name,
				&bot.DeviceName,
				&bot.Status,
				&bot.CPUPercent,
				&bot.MemoryPercent,
				&bot.UptimeSeconds,
				&bot.LastHeartbeat,
			); err != nil {
				return fiber.NewError(
					fiber.StatusInternalServerError,
					"failed to read bot",
				)
			}
			bots = append(bots, bot)
		}

		if err := rows.Err(); err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"failed to iterate bots",
			)
		}

		return c.JSON(fiber.Map{
			"value": bots,
			"count": len(bots),
		})
	})

	app.Get("/api/bots/live", func(c *fiber.Ctx) error {
		simulatorURL := os.Getenv("SIMULATOR_URL")
		if simulatorURL == "" {
			simulatorURL = "http://localhost:8788"
		}

		ctx, cancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cancel()

		req, err := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			simulatorURL+"/bots",
			nil,
		)
		if err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"failed to create simulator request",
			)
		}

		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return fiber.NewError(
				fiber.StatusBadGateway,
				"bot simulator unavailable",
			)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return fiber.NewError(
				fiber.StatusBadGateway,
				"bot simulator returned an error",
			)
		}

		type LiveBot struct {
			ID            string  `json:"id"`
			Name          string  `json:"name"`
			DeviceName    string  `json:"deviceName"`
			Status        string  `json:"status"`
			CPUPercent    float64 `json:"cpuPercent"`
			MemoryPercent float64 `json:"memoryPercent"`
			UptimeSeconds int64   `json:"uptimeSeconds"`
		}

		var bots []LiveBot
		if err := json.NewDecoder(
			io.LimitReader(resp.Body, 1024*1024),
		).Decode(&bots); err != nil {
			return fiber.NewError(
				fiber.StatusBadGateway,
				"invalid simulator response",
			)
		}

		if bots == nil {
			return fiber.NewError(
				fiber.StatusBadGateway,
				"simulator returned an invalid bot list",
			)
		}

		// Validate the entire response before updating the database.
		for _, bot := range bots {
			switch bot.Status {
			case "online", "offline", "starting", "stopping", "error":
			default:
				return fiber.NewError(
					fiber.StatusBadGateway,
					"simulator returned an invalid bot status",
				)
			}

			if bot.ID == "" ||
				bot.CPUPercent < 0 || bot.CPUPercent > 100 ||
				bot.MemoryPercent < 0 || bot.MemoryPercent > 100 ||
				bot.UptimeSeconds < 0 {
				return fiber.NewError(
					fiber.StatusBadGateway,
					"simulator returned invalid bot metrics",
				)
			}
		}

		tx, err := db.Begin(ctx)
		if err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"failed to start database transaction",
			)
		}
		defer tx.Rollback(context.Background())

		for _, bot := range bots {
			tag, err := tx.Exec(ctx, `
				UPDATE oxide_bot_agents
				SET status = $1,
				    cpu_percent = $2,
				    memory_percent = $3,
				    uptime_seconds = $4,
				    last_heartbeat = now(),
				    updated_at = now()
				WHERE id = $5
			`,
				bot.Status,
				bot.CPUPercent,
				bot.MemoryPercent,
				bot.UptimeSeconds,
				bot.ID,
			)
			if err != nil {
				return fiber.NewError(
					fiber.StatusInternalServerError,
					"failed to sync bot metrics",
				)
			}

			if tag.RowsAffected() != 1 {
				return fiber.NewError(
					fiber.StatusBadGateway,
					"simulator returned an unknown bot",
				)
			}
		}

		if err := tx.Commit(ctx); err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"failed to commit bot sync",
			)
		}

		return c.JSON(fiber.Map{
			"source": "simulator",
			"bots":   bots,
			"count":  len(bots),
		})
	})

	// Send a command to a bot through the provided simulator.
	app.Post("/api/bots/:id/commands", func(c *fiber.Ctx) error {
		botID := c.Params("id")

		var payload struct {
			Command string `json:"command"`
		}
		if err := c.BodyParser(&payload); err != nil {
			return fiber.NewError(
				fiber.StatusBadRequest, "invalid request body",
			)
		}

		switch payload.Command {
		case "start", "stop", "restart", "status":
		default:
			return fiber.NewError(
				fiber.StatusBadRequest, "invalid command",
			)
		}

		ctx, cancel := context.WithTimeout(
			context.Background(), 10*time.Second,
		)
		defer cancel()

		// Lock the bot row so concurrent commands for the
		// same bot are checked sequentially.
		tx, err := db.Begin(ctx)
		if err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"failed to start command transaction",
			)
		}
		defer tx.Rollback(context.Background())

		var botStatus string
		err = tx.QueryRow(ctx, `
            SELECT status
            FROM oxide_bot_agents
            WHERE id = $1
            FOR UPDATE
        `, botID).Scan(&botStatus)

		if err == pgx.ErrNoRows {
			return fiber.NewError(
				fiber.StatusNotFound,
				"bot not found",
			)
		}
		if err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"database error",
			)
		}

		var pendingCommand bool
		err = tx.QueryRow(ctx, `
            SELECT EXISTS (
                SELECT 1
                FROM oxide_bot_commands
                WHERE bot_id = $1
                  AND status IN ('queued', 'running')
                  AND command IN ('start', 'stop', 'restart')
            )
        `, botID).Scan(&pendingCommand)

		if err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"failed to check pending commands",
			)
		}

		if pendingCommand {
			return fiber.NewError(
				fiber.StatusConflict,
				"another control command is already in progress for this bot",
			)
		}

		// Prevent commands that are redundant for the current bot status.
		if payload.Command == "start" || payload.Command == "stop" {
			simulatorURL := os.Getenv("SIMULATOR_URL")
			if simulatorURL == "" {
				simulatorURL = "http://localhost:8788"
			}

			req, err := http.NewRequestWithContext(
				ctx,
				http.MethodGet,
				simulatorURL+"/bots",
				nil,
			)
			if err != nil {
				return fiber.NewError(
					fiber.StatusInternalServerError,
					"failed to create simulator request",
				)
			}

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fiber.NewError(
					fiber.StatusBadGateway,
					"simulator unavailable",
				)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return fiber.NewError(
					fiber.StatusBadGateway,
					"simulator returned an error",
				)
			}

			var simulatorResponse struct {
				Value []struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				} `json:"value"`
			}

			var bots []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return fiber.NewError(
					fiber.StatusBadGateway,
					"failed to read simulator response",
				)
			}

			if err := json.Unmarshal(body, &simulatorResponse); err == nil &&
				simulatorResponse.Value != nil {
				bots = simulatorResponse.Value
			} else if err := json.Unmarshal(body, &bots); err != nil {
				log.Printf("Simulator response decode error: %v", err)
				return fiber.NewError(
					fiber.StatusBadGateway,
					"invalid simulator response",
				)
			}

			currentStatus := ""
			for _, bot := range bots {
				if bot.ID == botID {
					currentStatus = bot.Status
					break
				}
			}

			if currentStatus == "" {
				return fiber.NewError(
					fiber.StatusNotFound,
					"bot not found in simulator",
				)
			}

			if payload.Command == "start" && currentStatus == "online" {
				return fiber.NewError(
					fiber.StatusConflict,
					"bot is already online",
				)
			}

			if payload.Command == "stop" && currentStatus == "offline" {
				return fiber.NewError(
					fiber.StatusConflict,
					"bot is already offline",
				)
			}

		}

		actor := c.Get("X-Actor")
		if actor == "" {
			actor = "local-user"
		}

		// Record the request before calling the simulator.
		var commandID int64
		err = tx.QueryRow(ctx, `
            INSERT INTO oxide_bot_commands
              (bot_id, command, requested_by, status)
            VALUES ($1, $2, $3, 'queued')
            RETURNING id
        `, botID, payload.Command, actor).Scan(&commandID)

		if err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"failed to record command",
			)
		}

		// Commit the queued command and release the bot row lock.
		if err := tx.Commit(ctx); err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"failed to save command",
			)
		}

		// Mark the command as running.
		_, err = db.Exec(ctx, `
            UPDATE oxide_bot_commands
            SET status = 'running'
            WHERE id = $1
        `, commandID)

		if err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"failed to update command status",
			)
		}

		simulatorURL := os.Getenv("SIMULATOR_URL")
		if simulatorURL == "" {
			simulatorURL = "http://localhost:8788"
		}

		requestBody, _ := json.Marshal(map[string]string{
			"command": payload.Command,
		})

		req, err := http.NewRequestWithContext(
			ctx,
			http.MethodPost,
			simulatorURL+"/bots/"+botID+"/commands",
			bytes.NewReader(requestBody),
		)
		if err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"failed to create simulator request",
			)
		}
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{Timeout: 3 * time.Second}
		resp, callErr := client.Do(req)

		status := "failed"
		var result json.RawMessage
		httpStatus := fiber.StatusBadGateway

		if callErr == nil {
			defer resp.Body.Close()
			body, readErr := io.ReadAll(
				io.LimitReader(resp.Body, 1024*1024),
			)
			if readErr == nil && json.Valid(body) {
				result = json.RawMessage(body)
			} else {
				result = json.RawMessage(
					`{"error":"invalid_simulator_response"}`,
				)
			}

			if resp.StatusCode >= 200 && resp.StatusCode < 300 &&
				readErr == nil && json.Valid(body) {
				status = "success"
				httpStatus = fiber.StatusOK
			}
		} else {
			result = json.RawMessage(
				`{"error":"simulator_unavailable_or_timeout"}`,
			)
		}

		// Persist the result, including failures.
		updateCtx, updateCancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer updateCancel()

		_, updateErr := db.Exec(updateCtx, `
			UPDATE oxide_bot_commands
			SET status = $1, result = $2::jsonb,
			    completed_at = now()
			WHERE id = $3
		`, status, string(result), commandID)
		if updateErr != nil {
			log.Printf("update command %d: %v", commandID, updateErr)
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"command result could not be saved",
			)
		}

		metadata, _ := json.Marshal(map[string]any{
			"commandId": commandID,
			"command":   payload.Command,
			"status":    status,
		})
		_, auditErr := db.Exec(updateCtx, `
			INSERT INTO audit_logs
				(actor, action, target_type, target_id, metadata)
			VALUES ($1, $2, 'bot', $3, $4::jsonb)
		`, actor, "bot.command."+status, botID, string(metadata))
		if auditErr != nil {
			log.Printf("audit command %d: %v", commandID, auditErr)
		}

		response := fiber.Map{
			"commandId": commandID,
			"botId":     botID,
			"command":   payload.Command,
			"status":    status,
			"result":    result,
		}
		if callErr != nil {
			response["warning"] = "Simulator outcome may be unknown; do not blindly retry."
		}

		if status != "success" {
			return c.Status(httpStatus).JSON(response)
		}
		return c.JSON(response)
	})

	// Retrieve command history for a bot.
	app.Get("/api/bots/:id/commands", func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cancel()

		rows, err := db.Query(ctx, `
			SELECT id, bot_id, command, requested_by, status,
			       result, created_at, completed_at
			FROM oxide_bot_commands
			WHERE bot_id = $1
			ORDER BY created_at DESC
			LIMIT 100
		`, c.Params("id"))
		if err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError, "failed to query commands",
			)
		}
		defer rows.Close()

		type CommandRecord struct {
			ID          int64           `json:"id"`
			BotID       string          `json:"botId"`
			Command     string          `json:"command"`
			RequestedBy string          `json:"requestedBy"`
			Status      string          `json:"status"`
			Result      json.RawMessage `json:"result"`
			CreatedAt   time.Time       `json:"createdAt"`
			CompletedAt *time.Time      `json:"completedAt"`
		}

		commands := make([]CommandRecord, 0)
		for rows.Next() {
			var item CommandRecord
			var result []byte

			if err := rows.Scan(
				&item.ID, &item.BotID, &item.Command,
				&item.RequestedBy, &item.Status, &result,
				&item.CreatedAt, &item.CompletedAt,
			); err != nil {
				return fiber.NewError(
					fiber.StatusInternalServerError, "failed to read commands",
				)
			}
			if len(result) > 0 {
				item.Result = json.RawMessage(result)
			}
			commands = append(commands, item)
		}

		if err := rows.Err(); err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError, "failed to iterate commands",
			)
		}

		return c.JSON(fiber.Map{
			"botId": c.Params("id"),
			"value": commands,
			"count": len(commands),
		})
	})

	// List orders.
	app.Get("/api/orders", func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cancel()

		rows, err := db.Query(ctx, `
			SELECT id, external_order_id, status, amount,
			       currency, created_at, updated_at
			FROM orders
			ORDER BY created_at DESC, id DESC
			LIMIT 100
		`)
		if err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"failed to query orders",
			)
		}
		defer rows.Close()

		type Order struct {
			ID              int64     `json:"id"`
			ExternalOrderID string    `json:"externalOrderId"`
			Status          string    `json:"status"`
			Amount          float64   `json:"amount"`
			Currency        string    `json:"currency"`
			CreatedAt       time.Time `json:"createdAt"`
			UpdatedAt       time.Time `json:"updatedAt"`
		}

		orders := make([]Order, 0)

		for rows.Next() {
			var order Order
			if err := rows.Scan(
				&order.ID,
				&order.ExternalOrderID,
				&order.Status,
				&order.Amount,
				&order.Currency,
				&order.CreatedAt,
				&order.UpdatedAt,
			); err != nil {
				return fiber.NewError(
					fiber.StatusInternalServerError,
					"failed to read order",
				)
			}
			orders = append(orders, order)
		}

		if err := rows.Err(); err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"failed to iterate orders",
			)
		}

		return c.JSON(fiber.Map{
			"value": orders,
			"count": len(orders),
		})
	})

	// Create an order unless the global kill-switch is enabled.
	app.Post("/api/orders", func(c *fiber.Ctx) error {
		if killSwitch.Load() {
			return c.Status(fiber.StatusServiceUnavailable).JSON(
				fiber.Map{
					"error":   "orders_paused",
					"message": "New orders are temporarily disabled",
				},
			)
		}

		var payload struct {
			ExternalOrderID string  `json:"externalOrderId"`
			Amount          float64 `json:"amount"`
			Currency        string  `json:"currency"`
		}

		if err := c.BodyParser(&payload); err != nil {
			return fiber.NewError(
				fiber.StatusBadRequest,
				"invalid request body",
			)
		}

		if payload.ExternalOrderID == "" {
			return fiber.NewError(
				fiber.StatusBadRequest,
				"externalOrderId is required",
			)
		}
		if payload.Amount <= 0 {
			return fiber.NewError(
				fiber.StatusBadRequest,
				"amount must be greater than zero",
			)
		}

		if payload.Currency == "" {
			payload.Currency = "THB"
		}
		if len(payload.Currency) != 3 {
			return fiber.NewError(
				fiber.StatusBadRequest,
				"currency must contain 3 characters",
			)
		}

		ctx, cancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cancel()

		tx, err := db.Begin(ctx)
		if err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"failed to start order transaction",
			)
		}
		defer tx.Rollback(context.Background())

		var orderID int64
		var orderStatus string
		var orderAmount float64
		var orderCurrency string

		err = tx.QueryRow(ctx, `
			INSERT INTO orders
				(external_order_id, status, amount, currency)
			VALUES ($1, 'pending', $2, $3)
			ON CONFLICT (external_order_id) DO NOTHING
			RETURNING id, status, amount, currency
		`,
			payload.ExternalOrderID,
			payload.Amount,
			payload.Currency,
		).Scan(
			&orderID,
			&orderStatus,
			&orderAmount,
			&orderCurrency,
		)

		if err != nil {
			// No returned row means this external order ID already exists.
			if err.Error() == "no rows in result set" {
				err = tx.QueryRow(ctx, `
					SELECT id, status, amount, currency
					FROM orders
					WHERE external_order_id = $1
				`, payload.ExternalOrderID).Scan(
					&orderID,
					&orderStatus,
					&orderAmount,
					&orderCurrency,
				)
				if err != nil {
					return fiber.NewError(
						fiber.StatusInternalServerError,
						"failed to retrieve existing order",
					)
				}

				if orderAmount != payload.Amount ||
					orderCurrency != payload.Currency {
					return fiber.NewError(
						fiber.StatusConflict,
						"externalOrderId already exists with different order data",
					)
				}

				if err := tx.Commit(ctx); err != nil {
					return fiber.NewError(
						fiber.StatusInternalServerError,
						"failed to complete order request",
					)
				}

				return c.JSON(fiber.Map{
					"id":              orderID,
					"externalOrderId": payload.ExternalOrderID,
					"status":          orderStatus,
					"amount":          orderAmount,
					"currency":        orderCurrency,
					"idempotent":      true,
				})
			}

			return fiber.NewError(
				fiber.StatusInternalServerError,
				"failed to create order",
			)
		}

		eventPayload, _ := json.Marshal(fiber.Map{
			"status": "pending",
		})

		_, err = tx.Exec(ctx, `
			INSERT INTO order_events (order_id, event_type, payload)
			VALUES ($1, 'ORDER_CREATED', $2::jsonb)
		`, orderID, string(eventPayload))
		if err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"failed to record order event",
			)
		}

		if err := tx.Commit(ctx); err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"failed to commit order",
			)
		}

		return c.Status(fiber.StatusCreated).JSON(fiber.Map{
			"id":              orderID,
			"externalOrderId": payload.ExternalOrderID,
			"status":          orderStatus,
			"amount":          orderAmount,
			"currency":        orderCurrency,
			"idempotent":      false,
		})
	})

	// Get the event timeline for an order.
	app.Get("/api/orders/:id/events", func(c *fiber.Ctx) error {
		orderID, err := strconv.ParseInt(c.Params("id"), 10, 64)
		if err != nil || orderID <= 0 {
			return fiber.NewError(
				fiber.StatusBadRequest,
				"invalid order ID",
			)
		}

		ctx, cancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cancel()

		var exists bool
		err = db.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM orders WHERE id = $1)
		`, orderID).Scan(&exists)
		if err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"failed to check order",
			)
		}
		if !exists {
			return fiber.NewError(
				fiber.StatusNotFound,
				"order not found",
			)
		}

		rows, err := db.Query(ctx, `
			SELECT id, event_type, payload, created_at
			FROM order_events
			WHERE order_id = $1
			ORDER BY created_at ASC, id ASC
		`, orderID)
		if err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"failed to query order events",
			)
		}
		defer rows.Close()

		type OrderEvent struct {
			ID        int64           `json:"id"`
			EventType string          `json:"eventType"`
			Payload   json.RawMessage `json:"payload"`
			CreatedAt time.Time       `json:"createdAt"`
		}

		events := make([]OrderEvent, 0)

		for rows.Next() {
			var event OrderEvent
			var payload []byte

			if err := rows.Scan(
				&event.ID,
				&event.EventType,
				&payload,
				&event.CreatedAt,
			); err != nil {
				return fiber.NewError(
					fiber.StatusInternalServerError,
					"failed to read order event",
				)
			}

			if len(payload) > 0 {
				event.Payload = json.RawMessage(payload)
			}
			events = append(events, event)
		}

		if err := rows.Err(); err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"failed to iterate order events",
			)
		}

		return c.JSON(fiber.Map{
			"orderId": orderID,
			"value":   events,
			"count":   len(events),
		})
	})

	// Update an order status and record the change atomically.
	app.Patch("/api/orders/:id/status", func(c *fiber.Ctx) error {
		orderID, err := strconv.ParseInt(c.Params("id"), 10, 64)
		if err != nil || orderID <= 0 {
			return fiber.NewError(
				fiber.StatusBadRequest,
				"invalid order ID",
			)
		}

		var payload struct {
			Status string `json:"status"`
		}
		if err := c.BodyParser(&payload); err != nil {
			return fiber.NewError(
				fiber.StatusBadRequest,
				"invalid request body",
			)
		}

		switch payload.Status {
		case "processing", "completed", "failed", "cancelled":
		default:
			return fiber.NewError(
				fiber.StatusBadRequest,
				"invalid target status",
			)
		}

		ctx, cancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cancel()

		tx, err := db.Begin(ctx)
		if err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"failed to start transaction",
			)
		}
		defer tx.Rollback(context.Background())

		var currentStatus string
		err = tx.QueryRow(ctx, `
			SELECT status
			FROM orders
			WHERE id = $1
			FOR UPDATE
		`, orderID).Scan(&currentStatus)

		if err == pgx.ErrNoRows {
			return fiber.NewError(
				fiber.StatusNotFound,
				"order not found",
			)
		}
		if err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"failed to retrieve order",
			)
		}
		allowed := canTransition(currentStatus, payload.Status)

		if !allowed {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{
				"error":           "invalid_status_transition",
				"currentStatus":   currentStatus,
				"requestedStatus": payload.Status,
			})
		}

		_, err = tx.Exec(ctx, `
			UPDATE orders
			SET status = $1, updated_at = now()
			WHERE id = $2
		`, payload.Status, orderID)
		if err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"failed to update order",
			)
		}

		eventPayload, _ := json.Marshal(fiber.Map{
			"from": currentStatus,
			"to":   payload.Status,
		})

		_, err = tx.Exec(ctx, `
			INSERT INTO order_events (order_id, event_type, payload)
			VALUES ($1, 'ORDER_STATUS_CHANGED', $2::jsonb)
		`, orderID, string(eventPayload))
		if err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"failed to record order event",
			)
		}

		if err := tx.Commit(ctx); err != nil {
			return fiber.NewError(
				fiber.StatusInternalServerError,
				"failed to commit status change",
			)
		}

		return c.JSON(fiber.Map{
			"orderId":        orderID,
			"previousStatus": currentStatus,
			"status":         payload.Status,
		})
	})

	app.Static("/", "../dashboard")
	log.Fatal(app.Listen(":" + appPort))
}
