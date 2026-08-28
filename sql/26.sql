DELETE FROM public.sys_ui_elements
WHERE element_code IN ('data-service_rule-engine', 'data-service', 'test_kan-ban-test')
   OR route_path IN ('view.data-service_rule-engine', 'view.test_kan-ban-test');
