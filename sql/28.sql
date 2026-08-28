ALTER TABLE public.market_bundle_installations
    ADD COLUMN IF NOT EXISTS request_hash varchar(64) NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS public.market_install_notification_outbox (
    id uuid PRIMARY KEY,
    installation_id uuid NOT NULL REFERENCES public.market_bundle_installations(id) ON DELETE CASCADE,
    tenant_id varchar(100) NOT NULL,
    bundle_key varchar(63) NOT NULL,
    bundle_version varchar(128) NOT NULL,
    market_token text NOT NULL,
    status varchar(16) NOT NULL DEFAULT 'pending',
    claim_token uuid,
    attempts integer NOT NULL DEFAULT 0,
    next_retry_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    lease_expires_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    delivered_at timestamptz,
    CONSTRAINT market_install_notification_install_unique UNIQUE (installation_id),
    CONSTRAINT market_install_notification_status_check CHECK (status IN ('pending', 'processing', 'delivered', 'credential_required')),
    CONSTRAINT market_install_notification_attempts_check CHECK (attempts >= 0),
    CONSTRAINT market_install_notification_claim_check CHECK (
        (status = 'processing' AND claim_token IS NOT NULL AND lease_expires_at IS NOT NULL)
        OR (status <> 'processing' AND claim_token IS NULL AND lease_expires_at IS NULL)
    ),
    CONSTRAINT market_install_notification_delivered_check CHECK (
        (status = 'delivered' AND delivered_at IS NOT NULL)
        OR (status <> 'delivered' AND delivered_at IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS market_install_notification_due_idx
    ON public.market_install_notification_outbox (status, next_retry_at, lease_expires_at, created_at, id)
    WHERE status <> 'delivered';

ALTER TABLE public.one_time_tasks
    ADD COLUMN IF NOT EXISTS claim_token uuid,
    ADD COLUMN IF NOT EXISTS lease_expires_at timestamptz,
    ADD COLUMN IF NOT EXISTS claim_attempts integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_error text;

ALTER TABLE public.periodic_tasks
    ADD COLUMN IF NOT EXISTS claim_token uuid,
    ADD COLUMN IF NOT EXISTS lease_expires_at timestamptz,
    ADD COLUMN IF NOT EXISTS claim_attempts integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_error text;

ALTER TABLE public.one_time_tasks
    ADD CONSTRAINT one_time_tasks_claim_attempts_check CHECK (claim_attempts >= 0),
    ADD CONSTRAINT one_time_tasks_claim_lease_check CHECK (
        (claim_token IS NULL AND lease_expires_at IS NULL)
        OR (claim_token IS NOT NULL AND lease_expires_at IS NOT NULL)
    );

ALTER TABLE public.periodic_tasks
    ADD CONSTRAINT periodic_tasks_claim_attempts_check CHECK (claim_attempts >= 0),
    ADD CONSTRAINT periodic_tasks_claim_lease_check CHECK (
        (claim_token IS NULL AND lease_expires_at IS NULL)
        OR (claim_token IS NOT NULL AND lease_expires_at IS NOT NULL)
    );

CREATE INDEX IF NOT EXISTS one_time_tasks_due_claim_idx
    ON public.one_time_tasks (execution_time, lease_expires_at)
    WHERE enabled = 'Y' AND executing_state = 'NEX';

CREATE INDEX IF NOT EXISTS periodic_tasks_due_claim_idx
    ON public.periodic_tasks (execution_time, lease_expires_at)
    WHERE enabled = 'Y';
