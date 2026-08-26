-- SYS_ADMIN is the platform-wide administrator. Tenant business routes are
-- visible in its global scope; the API enforces the selected tenant for writes.
UPDATE public.sys_ui_elements
SET authority = (authority::jsonb || '["SYS_ADMIN"]'::jsonb)::json
WHERE authority::jsonb ? 'TENANT_ADMIN'
  AND NOT authority::jsonb ? 'SYS_ADMIN';
