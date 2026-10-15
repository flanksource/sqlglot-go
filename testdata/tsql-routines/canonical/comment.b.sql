-- Marks an order as touched.
CREATE PROCEDURE acme.touch_order
    @order_id INT -- the order
AS
BEGIN
    /* one statement */
    UPDATE acme.orders SET touched_at = SYSUTCDATETIME() WHERE order_id = @order_id;
END
