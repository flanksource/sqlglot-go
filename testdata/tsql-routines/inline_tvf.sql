CREATE FUNCTION acme.open_orders(@customer_id INT)
RETURNS TABLE
AS
RETURN
(
    SELECT o.order_id, o.created_at
    FROM acme.orders AS o
    WHERE o.customer_id = @customer_id
      AND o.status = N'open'
)
