package http

import (
	"context"
	"net/http"
	"time"

	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/jackc/pgx/v5/pgxpool"
)

type HealthHandler struct {
	pool      *pgxpool.Pool
	sqsClient *awssqs.Client
}

func NewHealthHandler(pool *pgxpool.Pool, sqsClient *awssqs.Client) *HealthHandler {
	return &HealthHandler{
		pool:      pool,
		sqsClient: sqsClient,
	}
}

// Live handles GET /health/live (process liveness)
func (h *HealthHandler) Live(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "UP",
	})
}

// Ready handles GET /health/ready (readiness of PostgreSQL and LocalStack SQS)
func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	checks := map[string]string{
		"postgres": "UP",
		"sqs":      "UP",
	}
	allReady := true

	// Check PostgreSQL
	if h.pool != nil {
		if err := h.pool.Ping(ctx); err != nil {
			checks["postgres"] = "DOWN: " + err.Error()
			allReady = false
		}
	} else {
		checks["postgres"] = "DOWN: pool not initialized"
		allReady = false
	}

	// Check SQS
	if h.sqsClient != nil {
		_, err := h.sqsClient.ListQueues(ctx, &awssqs.ListQueuesInput{
			MaxResults: int32Ptr(1),
		})
		if err != nil {
			checks["sqs"] = "DOWN: " + err.Error()
			allReady = false
		}
	} else {
		checks["sqs"] = "DOWN: client not initialized"
		allReady = false
	}

	statusCode := http.StatusOK
	statusStr := "UP"
	if !allReady {
		statusCode = http.StatusServiceUnavailable
		statusStr = "DOWN"
	}

	writeJSON(w, statusCode, map[string]interface{}{
		"status": statusStr,
		"checks": checks,
	})
}

func int32Ptr(i int32) *int32 {
	return &i
}
