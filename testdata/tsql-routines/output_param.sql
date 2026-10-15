CREATE PROCEDURE acme.count_orders
    @customer_id INT,
    @count INT OUTPUT,
    @last_id INT = NULL OUT
AS
BEGIN
    SET @count = (SELECT COUNT(*) FROM acme.orders WHERE customer_id = @customer_id);
    SELECT @last_id = MAX(order_id) FROM acme.orders WHERE customer_id = @customer_id;
    RETURN @@ROWCOUNT;
END
