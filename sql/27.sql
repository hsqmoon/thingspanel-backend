CREATE TABLE IF NOT EXISTS public.dashboard_delete_jobs (
    id varchar(36) PRIMARY KEY,
    tenant_id varchar(36) NOT NULL,
    dashboard_id varchar(99) NOT NULL,
    status varchar(16) NOT NULL DEFAULT 'pending',
    claim_token varchar(36),
    attempts integer NOT NULL DEFAULT 0,
    next_retry_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    lease_expires_at timestamptz,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    delivered_at timestamptz,
    CONSTRAINT dashboard_delete_jobs_tenant_dashboard_unique UNIQUE (tenant_id, dashboard_id),
    CONSTRAINT dashboard_delete_jobs_status_check CHECK (status IN ('pending', 'processing', 'delivered')),
    CONSTRAINT dashboard_delete_jobs_attempts_check CHECK (attempts >= 0),
    CONSTRAINT dashboard_delete_jobs_claim_check CHECK (
        (status = 'processing' AND claim_token IS NOT NULL AND lease_expires_at IS NOT NULL)
        OR (status <> 'processing' AND claim_token IS NULL AND lease_expires_at IS NULL)
    ),
    CONSTRAINT dashboard_delete_jobs_delivered_check CHECK (
        (status = 'delivered' AND delivered_at IS NOT NULL)
        OR (status <> 'delivered' AND delivered_at IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS dashboard_delete_jobs_due_idx
    ON public.dashboard_delete_jobs (status, next_retry_at, lease_expires_at, created_at, id)
    WHERE status <> 'delivered';
