CREATE PROCEDURE acme.require_order
    @order_id INT
AS
BEGIN
    IF @order_id IS NULL
        THROW 50001, N'order_id is required', 1;

    IF NOT EXISTS (SELECT 1 FROM acme.orders WHERE order_id = @order_id)
    BEGIN
        THROW 50404, 'order not found', 16;
    END

    IF @order_id < 0 THROW;
END
