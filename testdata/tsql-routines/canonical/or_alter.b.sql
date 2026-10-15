CREATE OR ALTER PROCEDURE acme.touch_order @order_id INT AS BEGIN UPDATE acme.orders SET touched_at = SYSUTCDATETIME() WHERE order_id = @order_id; END
