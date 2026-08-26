-- Remove the obsolete hidden menu whose component never existed. It caused the
-- frontend route adapter to emit an invalid-route warning on every login.
DELETE FROM public.casbin_rule
WHERE v1 = '64f684f1-390c-b5f2-9994-36895025df8a';

DELETE FROM public.sys_ui_elements
WHERE id = '64f684f1-390c-b5f2-9994-36895025df8a';

-- Logs created before recursive redaction was introduced may contain tokens,
-- passwords or device vouchers. Their metadata remains useful, but the unsafe
-- historical bodies must not remain readable in the operations UI.
UPDATE public.operation_logs
SET request_message = '[历史请求内容已在安全升级时移除]'
WHERE request_message IS NOT NULL AND request_message <> '';

UPDATE public.operation_logs
SET response_message = '[历史响应内容已在安全升级时移除]'
WHERE response_message IS NOT NULL AND response_message <> '';
