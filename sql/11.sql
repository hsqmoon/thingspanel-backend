-- ✅2025/9.9 增加可视化类型字段
ALTER TABLE public.boards ADD vis_type varchar(50) NULL;
COMMENT ON COLUMN public.boards.vis_type IS '可视化类型';

-- 邮件服务默认保持未配置和关闭，避免把示例凭据误认为可用配置。
INSERT INTO public.notification_services_config
(id, config, notice_type, status, remark)
VALUES('286a116e-c25f-0f4c-890a-8a72128ef355',
       '{"host":"","port":465,"from_password":"","from_email":"","ssl":true}'::json,
       'EMAIL',
       'CLOSE',
       '');
