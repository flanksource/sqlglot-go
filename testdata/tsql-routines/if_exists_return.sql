CREATE PROCEDURE acme.cancel_order
    @order_id INT
AS
BEGIN
    SET NOCOUNT ON;

    IF NOT EXISTS (SELECT 1 FROM acme.orders WHERE order_id = @order_id)
        RETURN;

    IF EXISTS (SELECT 1 FROM acme.orders WHERE order_id = @order_id AND status = N'cancelled') RETURN 1

    UPDATE acme.orders SET status = N'cancelled' WHERE order_id = @order_id;
    RETURN 0;
END
