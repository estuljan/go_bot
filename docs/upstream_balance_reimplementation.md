# Upstream balance feature brief for a new AI implementer

This brief summarizes the existing upstream balance feature and clarifies how to reimplement it, including a **real-time balance monitoring channel** that proactively pushes low-balance alerts.

## Business scope
- **Object**: `UpstreamGroup` (上游群) has its own balance in CNY.
- **Balance definition**: For a given day, starting balance minus the sum of (per-interface volume × rate) from upstream billing.
- **Lifecycle**: Supports manual top-up and deduction. Daily settlement updates the balance based on the prior day’s usage and sends a report to the group.

## Data model
- Balance record per `UpstreamGroup`, with fields for current balance, minimum balance threshold, created/updated timestamps.
- Balance log entries capturing operator ID, time, delta, resulting balance, operation type (credit/debit), and optional remark.
- MongoDB is the backing store; use transactions for atomic balance + log writes. Add indexes for `upstream_group_id`, `created_at`, and any unique operation id if implementing idempotency.

## Service contracts
- **Adjust balance**: `Adjust(ctx, groupID, delta, operatorID, remark)` (delta can be positive or negative). Performs transactionally: read, validate, apply delta, write log, return new balance and a boolean indicating whether it is below the minimum threshold.
- **Set minimum balance**: `SetMinBalance(ctx, groupID, threshold, operatorID)`; persists threshold and writes a log entry.
- **Get balance**: `Get(ctx, groupID)` returns balance, threshold, and last update info.
- **Daily settlement**: `SettleDaily(ctx, groupID, targetDate)` loads interface bindings, fetches per-interface daily summary from the payment/billing API, computes total deduction (volume × rate), applies a single debit via `Adjust`, and returns a textual report.
- **Idempotency and concurrency**: Support concurrent bot commands with Mongo transactions/locks; optionally accept an `operation_id` for idempotent adjustments.

## Bot commands (admin-only)
- `+<amount>`: credit balance.
- `-<amount>`: debit balance.
- `/余额`: query balance and show whether it is below the threshold.
- `/set_min_balance <amount>`: configure minimum balance threshold.
- `/日结`: trigger manual settlement for the previous day and push the report.

## Real-time low-balance monitoring channel
- Add a lightweight watcher that pushes alerts without relying on manual commands:
  - **Trigger**: subscribe to balance change events emitted by `Adjust` (e.g., via a channel or message bus) and evaluate `balance < min_balance` after each successful adjustment. Additionally, schedule a periodic check (e.g., every 5–10 minutes) that reads balances for all groups to catch missed events.
  - **Stateful suppression**: keep per-group alert state to avoid spamming. Send an alert only when crossing from `ok → low`, and clear the flag when balance rises above the threshold; rate-limit to N alerts/hour.
  - **Delivery**: send Telegram bot messages to the upstream group chat. Include current balance, threshold, and a link or command hint for quick top-up.
  - **Configuration**: reuse the existing `/set_min_balance` threshold; optionally add `/set_balance_alert_limit <count_per_hour>` for rate limits.
  - **Resilience**: if the payment/billing service is disabled, the watcher still operates on locally stored balances and simply skips settlement-specific calls.

## Daily scheduler
- Run at 00:00:05 China Standard Time. For each upstream group:
  - Fetch prior day summary via payment service (if configured); otherwise, surface a clear error and skip.
  - Apply the total deduction through `Adjust` in one transaction.
  - Send the settlement report to the group. Retry per group up to three times on transient failures.

## Error handling and alerts
- When the payment service is not configured, guard all settlement entry points to return explicit errors instead of panicking.
- Log structured errors for failed adjustments, settlements, and alert deliveries.
- Include operator/user identifiers in logs for auditing.

## Integration notes
- Wire repositories, services, handlers, commands, and schedulers during bot initialization.
- Ensure Mongo indexes are created on startup.
- Keep formatting and linting consistent with Go standards (`gofmt`).
