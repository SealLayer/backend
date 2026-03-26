package httpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	apiv1 "github.com/SealLayer/backend/internal/api/v1"
	"github.com/SealLayer/backend/internal/config"
	"github.com/SealLayer/backend/internal/logger"
	"github.com/SealLayer/backend/internal/queue"
	"github.com/SealLayer/backend/internal/worker"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

type Server struct {
	cfg    config.Config
	logger *slog.Logger
	engine *gin.Engine

	q     *queue.Queue
	store *queue.BatchStore
	idem  *queue.IdempotencyStore

	worker *worker.Worker
}

func NewServer(cfg config.Config, log *slog.Logger) *Server {
	// Default Gin release mode; set GIN_MODE=debug for verbose Gin output.
	if strings.EqualFold(strings.TrimSpace(os.Getenv("GIN_MODE")), "debug") {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	engine := gin.New()
	engine.Use(gin.Recovery())
	engine.Use(corsMiddleware(cfg.CORSAllowedOrigins))
	engine.Use(requestLogMiddleware(log))

	s := &Server{
		cfg:    cfg,
		logger: log,
		engine: engine,
		q:      queue.New(cfg.QueueCapacity),
		store:  queue.NewBatchStore(),
		idem:   queue.NewIdempotencyStore(cfg.IdempotencyTTL),
	}

	s.worker = worker.New(cfg, log, s.q, s.store)
	s.registerRoutes()
	return s
}

// requestLogMiddleware correlates each request with request_id and logs duration and status to stdout.
func requestLogMiddleware(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := newRequestID()
		c.Set(logger.RequestID, rid)
		c.Writer.Header().Set("X-Request-ID", rid)

		start := time.Now()
		c.Next()

		log.Info("http request completed",
			logger.Op, "http_request",
			logger.RequestID, rid,
			"method", c.Request.Method,
			"url_path", c.Request.URL.Path,
			"matched_route", c.FullPath(),
			"status", c.Writer.Status(),
			"latency_ms", time.Since(start).Milliseconds(),
			"client_ip", c.ClientIP(),
		)
	}
}

// corsMiddleware enables browser cross-origin requests when CORS_ALLOWED_ORIGINS is set.
// Empty = no CORS headers (server-to-server, curl, same-origin reverse proxy unaffected).
func corsMiddleware(allowedOriginsEnv string) gin.HandlerFunc {
	s := strings.TrimSpace(allowedOriginsEnv)
	if s == "" {
		return func(c *gin.Context) { c.Next() }
	}

	cfg := cors.Config{
		AllowMethods:     []string{"GET", "POST", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization", "Idempotency-Key"},
		ExposeHeaders:    []string{"X-Request-ID"},
		AllowCredentials: false,
		MaxAge:           12 * 3600,
	}
	if s == "*" {
		cfg.AllowAllOrigins = true
	} else {
		var origins []string
		for _, p := range strings.Split(s, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				origins = append(origins, p)
			}
		}
		if len(origins) == 0 {
			return func(c *gin.Context) { c.Next() }
		}
		cfg.AllowOrigins = origins
	}
	return cors.New(cfg)
}

func newRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte("00000000"))
	}
	return hex.EncodeToString(b)
}

const (
	statusReasonQueued              = "queued"
	statusReasonBatching            = "batching"
	statusReasonPushed              = "pushed"
	statusReasonProcessing          = "processing"
	statusReasonQueueFull           = "queue_full"
	statusReasonInvalidJSON         = "invalid_json"
	statusReasonInvalidHash         = "invalid_hash"
	statusReasonMissingBatchID      = "batch_id_required"
	statusReasonNotFound            = "batch_not_found"
	statusReasonFailed              = "failed"
	statusReasonIdempotencyConflict = "idempotency_conflict"
)

func (s *Server) registerRoutes() {
	s.engine.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	v1 := s.engine.Group("/v1")
	{
		v1.POST("/seal", s.handleSeal())
		v1.GET("/status/:batch_id", s.handleStatus())
		v1.GET("/status/stream/:batch_id", s.handleStatusStream())
		v1.GET("/config/public", s.handlePublicConfig())
	}
}

func (s *Server) Run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.worker.Start(ctx)

	httpSrv := &http.Server{
		Addr:              s.cfg.HTTPListenAddr,
		Handler:           s.engine,
		ReadHeaderTimeout: 5 * time.Second,
	}

	s.logger.Info("http server listening",
		logger.Op, "http_listen",
		"addr", s.cfg.HTTPListenAddr,
	)

	err := httpSrv.ListenAndServe()
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Shutdown(ctx context.Context) error {
	// Placeholder for future graceful shutdown wiring.
	_ = ctx
	return nil
}

func (s *Server) handleSeal() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid, _ := c.Get(logger.RequestID)
		reqID, _ := rid.(string)

		var req apiv1.SealRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			s.logger.Warn("seal: invalid json",
				logger.Op, "seal_validate",
				logger.RequestID, reqID,
				"err", err,
			)
			c.JSON(http.StatusBadRequest, apiv1.SealResponse{Error: "invalid json", ReasonCode: statusReasonInvalidJSON})
			return
		}

		contentHash := strings.TrimSpace(strings.ToLower(req.ContentHash))
		if len(contentHash) != 64 {
			s.logger.Warn("seal: content_hash length invalid",
				logger.Op, "seal_validate",
				logger.RequestID, reqID,
				"len", len(contentHash),
			)
			c.JSON(http.StatusBadRequest, apiv1.SealResponse{Error: "content_hash must be 64 hex characters", ReasonCode: statusReasonInvalidHash})
			return
		}
		for _, r := range contentHash {
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
				s.logger.Warn("seal: content_hash character invalid",
					logger.Op, "seal_validate",
					logger.RequestID, reqID,
				)
				c.JSON(http.StatusBadRequest, apiv1.SealResponse{Error: "content_hash must be [0-9a-f] only", ReasonCode: statusReasonInvalidHash})
				return
			}
		}

		idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
		if idempotencyKey != "" {
			if ent, ok := s.idem.Get(idempotencyKey, time.Now().UTC()); ok {
				if ent.ContentHash != contentHash {
					c.JSON(http.StatusConflict, apiv1.SealResponse{
						BatchID:    ent.BatchID,
						StatusURL:  "/v1/status/" + ent.BatchID,
						Error:      "idempotency key reuse with different content_hash",
						ReasonCode: statusReasonIdempotencyConflict,
					})
					return
				}
				c.JSON(http.StatusAccepted, apiv1.SealResponse{
					BatchID:    ent.BatchID,
					StatusURL:  "/v1/status/" + ent.BatchID,
					Error:      "duplicate accepted",
					ReasonCode: statusReasonProcessing,
				})
				return
			}
		}

		resCh := make(chan queue.SealJobResult, 1)
		now := time.Now().UTC()
		batchID := queue.BatchIDForTime(now, s.cfg.BatchInterval)

		s.store.Put(queue.BatchState{
			BatchID:    batchID,
			Status:     queue.StatusQueued,
			ReasonCode: statusReasonQueued,
			Retryable:  true,
			UpdatedAt:  time.Now().UTC(),
		})
		job := &queue.SealJob{
			BatchID:     batchID,
			ContentHash: contentHash,
			RemoteIP:    c.ClientIP(),
			EnqueuedAt:  time.Now().UTC(),
			ResultCh:    resCh,
		}

		if ok := s.q.TryEnqueue(job); !ok {
			s.logger.Warn("seal: queue full",
				logger.Op, "seal_enqueue",
				logger.RequestID, reqID,
				logger.BatchID, batchID,
			)
			c.JSON(http.StatusServiceUnavailable, apiv1.SealResponse{Error: "queue full", ReasonCode: statusReasonQueueFull})
			return
		}
		if idempotencyKey != "" {
			s.idem.Put(idempotencyKey, contentHash, batchID, time.Now().UTC())
		}

		hashPrefix := contentHash
		if len(hashPrefix) > 12 {
			hashPrefix = hashPrefix[:12] + "…"
		}
		s.logger.Info("seal: job enqueued",
			logger.Op, "seal_enqueue",
			logger.RequestID, reqID,
			logger.BatchID, batchID,
			"content_hash_prefix", hashPrefix,
			"client_ip", c.ClientIP(),
		)

		ctx, cancel := context.WithTimeout(c.Request.Context(), s.cfg.RequestPendingMax)
		defer cancel()

		select {
		case <-ctx.Done():
			s.logger.Info("seal: pending timeout, processing in background",
				logger.Op, "seal_pending",
				logger.RequestID, reqID,
				logger.BatchID, batchID,
				"wait_max", s.cfg.RequestPendingMax.String(),
			)
			c.JSON(http.StatusAccepted, apiv1.SealResponse{
				BatchID:    batchID,
				StatusURL:  "/v1/status/" + batchID,
				Error:      "processing",
				ReasonCode: statusReasonProcessing,
			})
			return
		case res := <-resCh:
			if res.Err != nil {
				s.logger.Error("seal: operation failed",
					logger.Op, "seal_result",
					logger.RequestID, reqID,
					logger.BatchID, batchID,
					"err", res.Err,
				)
				c.JSON(http.StatusInternalServerError, apiv1.SealResponse{
					BatchID:    batchID,
					StatusURL:  "/v1/status/" + batchID,
					Error:      "failed",
					ReasonCode: statusReasonFailed,
				})
				return
			}
			s.logger.Info("seal: receipt ready",
				logger.Op, "seal_result",
				logger.RequestID, reqID,
				logger.BatchID, batchID,
				"ledger_path", res.LedgerPath,
				"commit_sha", res.CommitSHA,
			)
			c.JSON(http.StatusOK, apiv1.SealResponse{
				Receipt:    res.Receipt,
				BatchID:    batchID,
				StatusURL:  "/v1/status/" + batchID,
				ReasonCode: statusReasonPushed,
			})
			return
		}
	}
}

func (s *Server) handleStatus() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid, _ := c.Get(logger.RequestID)
		reqID, _ := rid.(string)

		batchID := strings.TrimSpace(c.Param("batch_id"))
		if batchID == "" {
			s.logger.Warn("status: batch_id missing",
				logger.Op, "status_query",
				logger.RequestID, reqID,
			)
			c.JSON(http.StatusBadRequest, apiv1.StatusResponse{
				BatchID:    "",
				Status:     "invalid",
				Error:      "batch_id required",
				ReasonCode: statusReasonMissingBatchID,
				UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
			})
			return
		}
		st, ok := s.store.Get(batchID)
		if !ok {
			s.logger.Info("status: batch not found",
				logger.Op, "status_query",
				logger.RequestID, reqID,
				logger.BatchID, batchID,
			)
			c.JSON(http.StatusNotFound, apiv1.StatusResponse{
				BatchID:    batchID,
				Status:     "not_found",
				ReasonCode: statusReasonNotFound,
				UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
			})
			return
		}
		s.logger.Info("status: response sent",
			logger.Op, "status_query",
			logger.RequestID, reqID,
			logger.BatchID, batchID,
			"state", string(st.Status),
			"ledger_path", st.LedgerPath,
			"commit_sha", st.CommitSHA,
		)
		c.JSON(http.StatusOK, statusResponseFromState(st))
	}
}

func (s *Server) handlePublicConfig() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, apiv1.PublicConfigResponse{
			ProtocolVersion:      "SIP-v1",
			PublicKeyFingerprint: s.cfg.GpgPublicKeyFingerprint,
			StatusPollIntervalMs: int64((s.cfg.BatchInterval / 2) / time.Millisecond),
			BatchIntervalMs:      int64(s.cfg.BatchInterval / time.Millisecond),
			ServerTimeUTC:        time.Now().UTC().Format(time.RFC3339),
		})
	}
}

func (s *Server) handleStatusStream() gin.HandlerFunc {
	return func(c *gin.Context) {
		batchID := strings.TrimSpace(c.Param("batch_id"))
		if batchID == "" {
			c.JSON(http.StatusBadRequest, apiv1.StatusResponse{
				BatchID:    "",
				Status:     "invalid",
				Error:      "batch_id required",
				ReasonCode: statusReasonMissingBatchID,
				UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
			})
			return
		}

		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		c.Writer.WriteHeader(http.StatusOK)

		if st, ok := s.store.Get(batchID); ok {
			if err := writeStatusEvent(c, statusResponseFromState(st)); err != nil {
				return
			}
			if st.Status == queue.StatusPushed || st.Status == queue.StatusFailed {
				return
			}
		} else {
			_ = writeStatusEvent(c, apiv1.StatusResponse{
				BatchID:    batchID,
				Status:     "not_found",
				ReasonCode: statusReasonNotFound,
				UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
			})
		}

		updates, cancel := s.store.Subscribe(batchID)
		defer cancel()
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-c.Request.Context().Done():
				return
			case st := <-updates:
				resp := statusResponseFromState(st)
				if err := writeStatusEvent(c, resp); err != nil {
					return
				}
				if st.Status == queue.StatusPushed || st.Status == queue.StatusFailed {
					return
				}
			case <-ticker.C:
				if _, err := c.Writer.Write([]byte(": keepalive\n\n")); err != nil {
					return
				}
				if f, ok := c.Writer.(http.Flusher); ok {
					f.Flush()
				}
			}
		}
	}
}

func statusResponseFromState(st queue.BatchState) apiv1.StatusResponse {
	return apiv1.StatusResponse{
		BatchID:    st.BatchID,
		Status:     string(st.Status),
		LedgerPath: st.LedgerPath,
		CommitSHA:  st.CommitSHA,
		Error:      st.Error,
		ReasonCode: st.ReasonCode,
		Retryable:  st.Retryable,
		UpdatedAt:  st.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func writeStatusEvent(c *gin.Context, payload apiv1.StatusResponse) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := c.Writer.Write([]byte(fmt.Sprintf("event: status\ndata: %s\n\n", raw))); err != nil {
		return err
	}
	if f, ok := c.Writer.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}
