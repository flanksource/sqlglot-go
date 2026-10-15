CREATE OR ALTER FUNCTION acme.order_total
(
    @order_id INT,
    @discount DECIMAL(5, 2) = 0
)
RETURNS DECIMAL(18, 2)
WITH SCHEMABINDING
AS
BEGIN
    DECLARE @total DECIMAL(18, 2);

    SELECT @total = SUM(l.quantity * l.unit_price)
    FROM acme.order_lines AS l
    WHERE l.order_id = @order_id;

    IF @total IS NULL RETURN 0;

    RETURN @total * (1 - @discount);
END
