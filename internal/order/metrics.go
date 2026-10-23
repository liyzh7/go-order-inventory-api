package order

import "time"

// StageRecorder receives aggregate-safe timing observations. Implementations
// must not retain request fields, credentials, order IDs, or other business
// data. A nil recorder disables the optional performance diagnostics path.
type StageRecorder interface {
	ObserveTransactionStage(stage, outcome string, duration time.Duration)
}

const (
	StageHTTPCreateTotal       = "http_create_total"
	StageValidationAndHash     = "validation_and_hash"
	StageServiceCreateTotal    = "service_create_total"
	StageAcquireConnection     = "acquire_connection"
	StageBeginTransaction      = "begin_transaction"
	StageIdempotencyLookup     = "idempotency_lookup"
	StageProductRead           = "product_read"
	StageOrderHeaderInsert     = "order_header_insert"
	StageInventoryUpdate       = "inventory_update"
	StageOrderItemInsert       = "order_item_insert"
	StageCommit                = "commit"
	StageTransactionAttempt    = "transaction_attempt"
	StageTransactionRetryDelay = "transaction_retry_delay"
)

const (
	StageOutcomeSuccess          = "success"
	StageOutcomeReplay           = "replay"
	StageOutcomeBusinessRejected = "business_rejected"
	StageOutcomeRetryableFailure = "retryable_failure"
	StageOutcomeFailure          = "failure"
)
