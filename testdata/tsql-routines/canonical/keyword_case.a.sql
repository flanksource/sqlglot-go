CREATE PROCEDURE acme.touch_order @order_id INT AS BEGIN IF @order_id IS NULL RETURN; UPDATE acme.orders SET touched_at = SYSUTCDATETIME() WHERE order_id = @order_id; END
