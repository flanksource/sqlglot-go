CREATE PROCEDURE acme.label_order @order_id INT AS BEGIN UPDATE acme.orders SET label = N'high  priority' WHERE order_id = @order_id; END
