-- Upstream seed data used masked-looking example SMTP credentials and marked
-- the service enabled. Replace only that known placeholder with an explicit
-- unconfigured/closed state; real administrator-provided credentials remain.
UPDATE public.notification_services_config
SET config = '{"host":"","port":465,"from_password":"","from_email":"","ssl":true}'::json,
    status = 'CLOSE'
WHERE notice_type = 'EMAIL'
  AND (
    config ->> 'from_email' LIKE '%***%'
    OR config ->> 'from_password' LIKE '%***%'
  );
