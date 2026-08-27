CREATE TABLE public.device_batch_outbox (
    event_id varchar(64) PRIMARY KEY,
    idempotency_key varchar(64) NOT NULL,
    tenant_id varchar(36) NOT NULL,
    service_access_id varchar(36) NOT NULL,
    service_access_ref_id varchar(36),
    destination varchar(255) NOT NULL,
    payload jsonb NOT NULL,
    status varchar(16) NOT NULL DEFAULT 'pending',
    claim_token varchar(36),
    attempts integer NOT NULL DEFAULT 0,
    next_retry_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    delivered_at timestamptz,
    CONSTRAINT device_batch_outbox_idempotency_key_unique UNIQUE (idempotency_key),
    CONSTRAINT device_batch_outbox_status_check CHECK (status IN ('pending', 'processing', 'delivered')),
    CONSTRAINT device_batch_outbox_pending_reference_check
        CHECK (status = 'delivered' OR service_access_ref_id IS NOT NULL),
    CONSTRAINT device_batch_outbox_service_access_fk
        FOREIGN KEY (service_access_ref_id) REFERENCES public.service_access(id) ON DELETE RESTRICT
);

CREATE INDEX device_batch_outbox_delivery_idx
    ON public.device_batch_outbox (status, next_retry_at, created_at, event_id)
    WHERE status <> 'delivered';
